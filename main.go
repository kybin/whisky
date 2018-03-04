package main

import (
	"fmt"
	"io/ioutil"
	"log"

	blackfriday "gopkg.in/russross/blackfriday.v2"
)

func main() {
	data, err := ioutil.ReadFile("testdata/basic.md")
	if err != nil {
		log.Fatal(err)
	}
	out := blackfriday.Run(data)
	fmt.Println(string(out))
}
