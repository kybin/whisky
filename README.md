# whisky

whisky is a wiki a little drunken.


## Installation

whisky is go gettable.

```
$ go get github.com/kybin/whisky
```

whisky is also vgo gettable. yay!

```
$ git clone https://github.com/kybin/whisky
$ vgo install
```

## Run

```
$ mkdir wiki
$ cd wiki
$ whisky -init
$ whisky -addr :80 # for test.
$ whisky -addr :80 -https -cert your/cert.pem -key your/key.pem # for real use.
```
