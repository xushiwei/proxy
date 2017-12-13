package main

import (
	"fmt"
	"net/http"
	"time"
)

func greetB(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "I am b server! now is %s\n", time.Now())
}

func main() {
	http.HandleFunc("/", greetB)
	err := http.ListenAndServe(":80", nil)
	if err != nil {
		fmt.Printf("ListenAndServe 2 %v\n", err)
	}
}
