package main

import (
	"fmt"
	"net/http"
	"proxy/revert"
)

func greetA(w http.ResponseWriter, r *http.Request) {
	// fmt.Fprintf(w, "I am a server! now is %s\n", time.Now())
	proxy, err := revert.New("localhost:80", "")
	if err != nil {
		fmt.Printf("revert.New %v\n", err)
		return
	}
	proxy.ServeHTTP(w, r)
}

func main() {
	http.HandleFunc("/proxy", greetA)
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		fmt.Printf("ListenAndServe %v\n", err)
	}
}
