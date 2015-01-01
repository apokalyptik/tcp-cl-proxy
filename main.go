package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"runtime"
	"sync"
	"time"
)

var listenOn = "127.0.0.1:8301"
var proxyTo = "127.0.0.1:8300"
var statsOn = "127.0.0.1:8299"

var concurrency = 1

var wCond = &sync.Cond{L: &sync.Mutex{}}
var waiting = 0
var active = 0

var concurrencyBucket chan struct{}

func init() {
	flag.StringVar(&listenOn, "l", listenOn, "Listen for TCP connections at this address")
	flag.StringVar(&proxyTo, "p", proxyTo, "Proxy connected clients to this address")
	flag.StringVar(&statsOn, "s", statsOn, "Give stats to clients connecting to this address")
	flag.IntVar(&concurrency, "c", concurrency, "Number of active connections allowed to proxy address at a given time")
}

func pre() {
	// Lock our condition
	wCond.L.Lock()
	defer wCond.L.Unlock()
	// Record that we're now in a wait state
	waiting++
	for active == concurrency {
		// Wait unlocks the conditions lock when called, and re-locks it upon returning.
		// Otherwise the entire program would deadlock here
		wCond.Wait()
	}
	// Record that we're no longer waiting
	waiting--
	// Record that we're actively processing the connection now.
	active++
}

func post() {
	// Lock our condition to avoid races when updating the active variable
	wCond.L.Lock()
	// Record that we're no longer active
	active--
	// Unlock our cond
	wCond.L.Unlock()
	// Send a signal to exactly one goroutine waiting on the cond (unless none are waiting
	// then this is effectively a no-op
	wCond.Signal()
}

func proxy(c net.Conn) {
	var err error
	var p net.Conn
	var dialedAt time.Time
	var doneAt time.Time
	var waitedAt time.Time

	var startAt = time.Now()
	var client = c.RemoteAddr().String()

	// Deferred functions are run in LIFO (last in first out) order.
	// So we make sure that our logging function runs last by
	// defining and deferring it first
	defer func() {
		now := time.Now()
		if err == nil {
			log.Printf(
				"client=%s status=success took=%f wait=%f dial=%f copy=%f",
				client,
				now.Sub(startAt).Seconds(),
				waitedAt.Sub(startAt).Seconds(),
				dialedAt.Sub(waitedAt).Seconds(),
				doneAt.Sub(dialedAt).Seconds())
		} else {
			log.Printf(
				"client=%s status=error took=%f message=\"%s\"",
				client,
				now.Sub(startAt).Seconds(),
				err.Error())
		}
	}()
	// Always close the client connection
	defer c.Close()
	// Always post-process (decriments the active counter, and signals to
	// other waiting goroutines that a new active slot is probably ready)
	defer post()

	// Pre-process (wait if necessay, incriment the active connection counter, etc)
	pre()
	waitedAt = time.Now()

	// Dial out to the real TCP service
	p, err = net.Dial("tcp", proxyTo)
	if err != nil {
		return
	}
	// If we ever get a connection we always need to close it.
	defer p.Close()
	dialedAt = time.Now()

	// Setup a waitgroup, because the two copies ( client->server, and server->client )
	// need to happen concurrently we'll launch both in new goroutines, and we need
	// something to signal that both operations have been completed
	var w sync.WaitGroup
	// We add 2 to the waitgroup here to avoid a race which would exist if we added it
	// inside our new goroutines
	w.Add(2)

	go func() {
		// Copy all bytes from the client to the proxied server
		io.Copy(p, c)
		w.Done()
	}()
	go func() {
		// Copy all bytes from the proxied server to the client
		io.Copy(c, p)
		w.Done()
	}()
	// Wait for both copy operations to complete
	w.Wait()
	// Record when we finished. This way we won't report any of the post
	// processing time that we took in the logs
	doneAt = time.Now()
}

func server() {
	// Bind our listening TCP socket
	ln, err := net.Listen("tcp", listenOn)
	if err != nil {
		log.Fatal("net.Listen error: " + err.Error())
	}
	// Setup our accept loop
	for {
		conn, err := ln.Accept()
		if err != nil {
			// I'm not exactly sure what could go wrong here but whatever it is
			// is probably bad...
			log.Fatal("net.Listener.Accept error: " + err.Error())
		}
		// Send our connection to be proxied in a new goroutine.
		go proxy(conn)
	}
}

func stats() {
	// Setup our listener. If we fail to do so we bail out before launching a goroutine.
	// to prevent races where the server is listening to clients (real clients) an but
	// will fatal unexpectedly while serving them because of this.
	ln, err := net.Listen("tcp", statsOn)
	if err != nil {
		log.Fatal("net.Listen error: " + err.Error())
	}
	go func(ln net.Listener) {
		// Accept clients in a loop
		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Fatal("net.Listener.Accept error: " + err.Error())
			}
			// Launch the handler for the client connection in a goroutine, to get back
			// to our loop quickly
			go func(c net.Conn) {
				// Spit out our stats and close the connection
				defer c.Close()
				fmt.Fprintf(c, "active: %d, waiting: %d\n", active, waiting)
			}(conn)
		}
	}(ln)
}

func main() {
	flag.Parse()
	runtime.GOMAXPROCS(runtime.NumCPU())
	stats()
	server()
}
