# Ifrit
Docs: https://godoc.org/github.com/joonnna/ifrit
## How to install golang
https://golang.org/doc/install

## How to setup golang environment
https://golang.org/doc/code.html

## Client
```go
import github.com/joonnna/ifrit
```

## Certificate authority
```go
import github.com/joonnna/ifrit/cauth
```

To download Ifrit simply issue the following command after including it in a src file "go get ./..."

## Starting up a client and certificate authority
```go
package main

import (
	"github.com/joonnna/ifrit/cauth"
	"github.com/joonnna/ifrit"
)

func main() {
    var numRings uint32 = 5

    ca, _ := cauth.NewCa()
    go ca.Start(numRings)

    config := &ifrit.Config{
        Ca: true,
        CaAddr: ca.Addr(),
    }

    c := client.NewClient(config)
    go c.Start()
}
```

## Starting up a clients with self signed certificates(no ca)
```go
package main

import (
	"github.com/joonnna/ifrit"
)

func main() {
    // nil config results in full defaults..
    // first client has no entry point, will operate alone.
    // with no ca, ifrit defaults to 32 rings.
    first, _ := ifrit.NewClient(nil)
    go first.Start()

    config := &ifrit.Config{
        EntryAddrs: []string{first.Addr()},
    }

    numClients := 5
    for i := 0; i < numClients; i++ {
        c, _ := ifrit.NewClient(config)
        go c.Start()
    }
}
```

## Example of adding gossip
Mockup application uses ifrit's gossip functionality for state convergence.
```go
package main

import (
    "bytes"
    "encoding/json"
    "time"

    "github.com/joonnna/ifrit"
)


// Mockup application
type application struct {
    ifritClient *ifrit.Client

    exitChan chan bool
    data     *appData
}

// Start the mockup application
func (a *application) Start() {
    a.ifritClient.RegisterMsgHandler(a.handleMessages)

    for {
        a.doStuff()
    }
}

// This callback will be invoked on each received message.
func (a *application) handleMessages(data []byte) ([]byte, error) {
    msg := data.Unmarshal()

    a.state = mergeState(msg)

    //Updating to a new state so that its propegated throughout the ifrit network
    a.ifritClient.SetGossipContent(a.state)

    return nil, nil
}
```


## Example of sending messages
Mockup application periodically sends messages participants in the network.
```go
package main

import (
    "bytes"
    "encoding/json"
    "time"

    "github.com/joonnna/ifrit"
)


// Mockup application
type application struct {
    ifritClient *ifrit.Client

    exitChan chan bool
    data     *appData
}


// Start the mockup application
func (a *application) Start() {
    a.ifritClient.RegisterMsgHandler(a.handleMessages)

    for {
        a.doStuff()

        msg := a.newStuff()

        members := a.ifritClient.Members()

        ch := a.ifritClient.SendTo(members[randIdx], msg)

        resp := <-ch
        a.handleResp(resp)
    }
}

// This callback will be invoked on each received message.
func (a *application) handleMessages(data []byte) ([]byte, error) {
    msg := data.Unmarshal()

    a.storeMsg(msg)

    return a.generateResponse(msg), nil
}
```





## Example of sending message to all
Mockup application periodically sends messages to all participants in the network.
```go
package main

import (
    "bytes"
    "encoding/json"
    "time"

    "github.com/joonnna/ifrit"
)


// Mockup application
type application struct {
    ifritClient *ifrit.Client

    exitChan chan bool
    data     *appData
}


// Start the mockup application
func (a *application) Start() {
    a.ifritClient.RegisterMsgHandler(a.handleMessages)

    for {
        a.doStuff()

        msg := a.newStuff()

        ch, num := a.ifritClient.SendToAll(msg)

        for i := 0; i < num; i++ {
            resp := <-ch
            a.handleResp(resp)
        }
    }
}

// This callback will be invoked on each received message.
func (a *application) handleMessages(data []byte) ([]byte, error) {
    msg := data.Unmarshal()

    a.storeMsg(msg)

    return a.generateResponse(msg), nil
}
```
