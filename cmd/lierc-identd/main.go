package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
)

var portmap = fmt.Sprintf("http://%s:5005/portmap", os.Getenv("LIERCD_HOST"))

func main() {
	ln, err := net.Listen("tcp", ":113")
	if err != nil {
		log.Panic(err)
	}

	log.Printf("listening on port 113")

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("Error accepting: %v", err)
			continue
		}
		go handleConnection(conn)
	}
}

func handleConnection(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)

READLINE:
	for {
		line, err := reader.ReadString('\n')

		if err == io.EOF {
			return
		}

		if err != nil {
			conn.Close()
			log.Printf("Failed to read line: %v", err)
			return
		}

		line = strings.TrimSuffix(line, "\r\n")
		log.Printf("%s", line)

		parts := strings.SplitN(line, ",", 2)

		if len(parts) != 2 {
			log.Printf("Invalid request: '%s'", line)
			out := fmt.Sprintf("%s : ERROR : INVALID-PORT\n", line)
			conn.Write([]byte(out))
			continue READLINE
		}

		local := strings.TrimSpace(parts[0])
		remote := strings.TrimSpace(parts[1])

		resp, err := http.Get(portmap)
		defer resp.Body.Close()

		decoder := json.NewDecoder(resp.Body)
		ports := make([][]string, 0)
		err = decoder.Decode(&ports)

		if err != nil {
			log.Printf("Error decoding portmap: %v", err)
			out := fmt.Sprintf("%s, %s : ERROR : UNKNOWN-ERROR\n", local, remote)
			conn.Write([]byte(out))
			continue READLINE
		}

		for _, mapping := range ports {
			if mapping[1] == local && mapping[2] == remote {
				out := fmt.Sprintf("%s, %s : USERID : UNIX : %s\n", local, remote, mapping[0])
				log.Printf(out)
				conn.Write([]byte(out))
				continue READLINE
			}
		}

		out := fmt.Sprintf("%s, %s : ERROR : NO-USER\n", local, remote)
		log.Printf(out)
		conn.Write([]byte(out))
	}
}
