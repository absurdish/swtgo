package httpcache

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// config for a caching middleware
type CacheConfig struct {
	// Time-To-Live defined cache duration
	// 	example:
	// 	TTL: 5*time.Minute,
	TTL time.Duration
	// list the methods to cache
	// 	example:
	// 	CacheableMethods: []string{"GET","PUT"}
	CacheableMethods     []string
	// list the status codes to cache
	// 	example: 
	// 	CacheableStatusCodes: []int{200}
	CacheableStatusCodes []int
}

// HTTP response for caches 
type cachedResponse struct {
	// cached HTTP response
	response     *http.Response
	// cached data
	body         []byte
	// date when the cache was last accessed
	lastAccessed time.Time
}

// CacheMiddleware implements http.RoundTripper with caching capabilities

//  cache middleware that implements roundtripper with caching capabilities
type CacheMiddleware struct {
	// roundtripper
	transport http.RoundTripper
	// cache configuration
	config    CacheConfig
	// map of cached responses
	cache     map[string]*cachedResponse
	// mutex for shared ownership
	mu        sync.RWMutex
	// ongoing requests
	ongoing   map[string]chan struct{}
	ongoingMu sync.Mutex
}

// checks if request method satisfies caching requirements
func (self *CacheMiddleware) isCacheableMethod(method string) bool {
	for _, m := range self.config.CacheableMethods {
		if m == method {
			return true
		}
	}
	return false
}

// checks if request status code satisfies caching requirements
func (self *CacheMiddleware) isCacheableStatus(statusCode int) bool {
	for _, c := range self.config.CacheableStatusCodes {
		if c == statusCode {
			return true
		}
	}
	return false
}

// roundtripper interface
func (self *CacheMiddleware) RoundTrip(req *http.Request) (*http.Response, error) {
	// skip caching if method is not cacheable
	if !self.isCacheableMethod(req.Method) {
		return self.transport.RoundTrip(req)
	}

	// acquire the unique key
	key := cacheKey(req)

	// check for ongoing requests
	self.ongoingMu.Lock()
	ch, exists := self.ongoing[key]
	if exists {
		self.ongoingMu.Unlock()
		<-ch // wait for the ongoing request to finish
		
		// btw rust's `if let` syntax is more beautiful 
		if resp := self.getCachedResponse(key); resp != nil {
			return resp, nil
		}
		// if cache miss after waiting, proceed with new request
	} else {
		// create channel for this request
		ch = make(chan struct{})
		self.ongoing[key] = ch
		self.ongoingMu.Unlock()

		// clean up ongoing request when done
		defer func() {
			self.ongoingMu.Lock()
			delete(self.ongoing, key)
			close(ch)
			self.ongoingMu.Unlock()
		}()
	}

	// check cache
	if resp := self.getCachedResponse(key); resp != nil {
		return resp, nil
	}

	// perform actual request
	resp, err := self.transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// cache the response if status code is cacheable
	if self.isCacheableStatus(resp.StatusCode) {
		if err := self.cacheResponse(key, resp); err != nil {
			resp.Body.Close()
			return nil, err
		}
	}

	return resp, nil
}

// gets the cached response (if it's valid)
func (self *CacheMiddleware) getCachedResponse(key string) *http.Response {
	self.mu.RLock()
	cached, exists := self.cache[key]
	self.mu.RUnlock()

	if !exists {
		return nil
	}

	// check if cache entry has expired
	if time.Since(cached.lastAccessed) > self.config.TTL {
		self.mu.Lock()
		delete(self.cache, key)
		self.mu.Unlock()
		return nil
	}

	// update last accessed time
	self.mu.Lock()
	cached.lastAccessed = time.Now()
	self.mu.Unlock()

	return cloneResponse(cached.response, cached.body)
}

// stores a response in the cache
func (m *CacheMiddleware) cacheResponse(key string, resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))

	cached := &cachedResponse{
		response:     resp,
		body:         body,
		lastAccessed: time.Now(),
	}

	m.mu.Lock()
	m.cache[key] = cached
	m.mu.Unlock()

	return nil
}

// generate a unique cache key for each request
func cacheKey(req *http.Request) string {
	return req.Method + ":" + req.URL.String()
}

// copies response with a new body
func cloneResponse(resp *http.Response, body []byte) *http.Response {
	clone := *resp
	clone.Body = io.NopCloser(bytes.NewReader(body))
	return &clone
}



// initialize a new caching middleware 
func initCacheMW(transport http.RoundTripper, config CacheConfig) *CacheMiddleware {
	// use default transport if `transport` argument isn't supplied
	if transport == nil {
		transport = http.DefaultTransport
	}

	// initialize struct fields with default values
	return &CacheMiddleware{
		transport: transport,
		config:    config,
		cache:     make(map[string]*cachedResponse),
		ongoing:   make(map[string]chan struct{}),
	}
}

//
// example of using this middleware:
//

func example(){
	// initialize the client
	client := &http.Client {
		Transport: initCacheMW(http.DefaultTransport, CacheConfig{
			TTL: 30 * time.Minute,
			CacheableMethods: []string{"GET"},
			CacheableStatusCodes: []int{200},
		}),
	}
	
	// make the response
	resp, err := client.Get("https://example.com/idk_something")
	if err != nil {log.Fatal(err)}
	// this wouldn't be needed in rust btw
	defer resp.Body.Close()
}
