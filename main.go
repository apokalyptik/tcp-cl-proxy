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
var count uint64

var concurrencyBucket chan struct{}

type client struct {
	ID   uint64
	name string
	conn net.Conn

	server net.Conn
	err    error

	w sync.WaitGroup

	didWait bool
	start   time.Time
	waited  time.Time
	dialed  time.Time
	done    time.Time
}

func (c *client) copyTo(conn net.Conn, done chan bool) {
	io.Copy(conn, c.conn)
	c.w.Done()
	done <- true

}

func (c *client) copyFrom(conn net.Conn, done chan bool) {
	io.Copy(c.conn, conn)
	c.w.Done()
	done <- true

}

func (c *client) copyAll() {
	done := make(chan bool)

	go c.copyTo(c.server, done)
	go c.copyFrom(c.server, done)

	//We wait until both ONE is done (an error or EOF can lead one of the sides to be done)

	<-done
	// Record when we finished. This way we won't report any of the post
	// processing time that we took in the logs
	c.done = time.Now()

}

func (c *client) doProxy() {
	// Dial out to the real TCP service
	c.server, c.err = net.Dial("tcp", proxyTo)
	if c.err != nil {
		c.logError()
		return
	}
	// If we ever get a connection we always need to close it.
	c.dialed = time.Now()
	c.copyAll()
	c.logSuccess()
}

func (c *client) logError() {
	now := time.Now()
	log.Printf(
		"client=%s num=%d status=error took=%f message=\"%s\"",
		c.name,
		c.ID,
		now.Sub(c.start).Seconds(),
		c.err.Error())
}

func (c *client) logSuccess() {
	now := time.Now()
	waited := 0.0
	if c.didWait {
		waited = c.waited.Sub(c.start).Seconds()
	}
	log.Printf(
		"client=%s num=%d status=success took=%f wait=%f dial=%f copy=%f",
		c.name,
		c.ID,
		now.Sub(c.start).Seconds(),
		waited,
		c.dialed.Sub(c.waited).Seconds(),
		c.done.Sub(c.dialed).Seconds())
}

func (c *client) setup() {
	c.w.Add(2)
	// Lock our condition
	wCond.L.Lock()
	defer wCond.L.Unlock()
	// Record that we're now in a wait state
	count++
	c.ID = count
	waiting++
	for active == concurrency {
		// Wait unlocks the conditions lock when called, and re-locks it upon returning.
		// Otherwise the entire program would deadlock here
		c.didWait = true
		wCond.Wait()
	}
	c.waited = time.Now()
	// Record that we're no longer waiting
	waiting--
	// Record that we're actively processing the connection now.
	active++
}

func (c *client) teardown() {
	log.Println("Teardown start")
	c.conn.Close()
	c.server.Close()
	log.Println("Proxy connection closed")
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

func (c *client) mind() {
	c.setup()
	c.doProxy()
	c.teardown()
}

func handleClient(conn net.Conn) {
	c := &client{
		name:  conn.RemoteAddr().String(),
		conn:  conn,
		start: time.Now(),
	}
	c.mind()
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
		go handleClient(conn)
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

func init() {
	flag.StringVar(&listenOn, "l", listenOn, "Listen for TCP connections at this address")
	flag.StringVar(&proxyTo, "p", proxyTo, "Proxy connected clients to this address")
	flag.StringVar(&statsOn, "s", statsOn, "Give stats to clients connecting to this address")
	flag.IntVar(&concurrency, "c", concurrency, "Number of active connections allowed to proxy address at a given time")
}

func main() {
	flag.Parse()
	runtime.GOMAXPROCS(runtime.NumCPU())
	stats()
	server()
}
