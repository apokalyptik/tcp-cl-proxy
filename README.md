tcp-cl-proxy
============

A TCP Proxy designed to limit concurrency

### Why?

Sometimes have a service listening on a TCP port which can be easily overwhelmed by concurrent requests. Additionally, for whatever reason, the service does not have any method for limiting its own resource usage or concurrency.  When you do not have the ability (closed, missing, or untouchable source code) or resources (time, money, etc) to properly fix the service yourself you can use this very simple proxy for limiting concurrency as a man in the middle.

### How does it work?

```
Usage of ./tcp-cl-proxy:
  -c=1: Number of active connections allowed to proxy address at a given time
  -l="127.0.0.1:8301": Listen for TCP connections at this address
  -p="127.0.0.1:8300": Proxy connected clients to this address
  -s="127.0.0.1:8299": Give stats to clients connecting to this address
```

When a new connection comes in and the number of active connections is already at the configured maximum the proxy simply accepts the new connection and waits until an active connection finishes. When a free active connection slot opens up one (and only one) new connection to the service is made to service one additional waiting client.

Additionally the proxy provides a second listening socket on which to test livliness and gather simple stats.  This port is good for use with things like  monit, nagios, munin, etc.  It simply returns the number of active and waiting connections, and then disconnects. 
