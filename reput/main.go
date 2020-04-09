// Copyright 2019 Google LLC
//
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file or at
// https://developers.google.com/open-source/licenses/bsd

package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kr/binarydist"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func main() {
	f, err := os.Open(os.Args[3])
	if err != nil {
		log.Fatalln("Failed to open file:", err)
	}
	defer f.Close()

	log.Println("Connecting via SSH...")
	sshc, err := sshConnect(os.Args[1], os.Args[2])
	if err != nil {
		log.Fatalln("Failed to connect via SSH:", err)
	}
	defer sshc.Close()

	log.Println("Checking xochitl...")
	hash, err := xochitlHash(sshc)
	if err != nil {
		log.Fatalln("Failed to hash xochitl:", err)
	}
	switch hash {
	case "c9434d88cab1d2af224d7c45bcb860ba426e5fb0ed4d60df96ceadfb56bd9b25":
		log.Println("Backing up xochitl to /home/root...")
		if err := xochitlBackup(sshc); err != nil {
			log.Fatalln("Failed to backup xochitl:", err)
		}
		log.Println("Patching xochitl with @nickmooney's patch...")
		if err := xochitlPatch(sshc); err != nil {
			log.Fatalln("Failed to patch xochitl:", err)
		}
		if err := xochitlRestart(sshc); err != nil {
			log.Fatalln("Failed to restart xochitl:", err)
		}
		fmt.Fprint(os.Stderr, "Enable the Web UI in Storage settings, and press enter: ")
		fmt.Scanln()
	case "79f67ea4ac8dbe0ce8baeb3c91bbbf7574c200bb75eb87c3c89b7f56eb849b89":
		// Version 2.1.1.3, already patched.
	default:
		log.Println("Warning: unknown xochitl version, Web UI might not be available at localhost.")
	}

	log.Println("Uploading file to Web UI...")
	if err := uploadFile(sshc.Dial, f); err != nil {
		log.Fatalln("Failed to upload file:", err)
	}

	log.Println("Success!")
}

func sshConnect(endpoint, fingerprint string) (*ssh.Client, error) {
	socket := os.Getenv("SSH_AUTH_SOCK")
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("failed to open SSH_AUTH_SOCK: %v", err)
	}

	agentClient := agent.NewClient(conn)
	config := &ssh.ClientConfig{
		User: "root",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeysCallback(agentClient.Signers),
		},
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			if fp := ssh.FingerprintSHA256(key); fp != fingerprint {
				return fmt.Errorf("incorrect host key: %v", fp)
			}
			return nil
		},
	}

	return ssh.Dial("tcp", endpoint, config)
}

func uploadFile(dial func(network, addr string) (net.Conn, error), f *os.File) error {
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	name := filepath.Base(f.Name())
	if fw, err := w.CreateFormFile("file", name); err != nil {
		return err
	} else if _, err := io.Copy(fw, f); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	client := &http.Client{
		Transport: &http.Transport{
			Dial:              dial,
			DisableKeepAlives: true,
		},
		Timeout: 1 * time.Minute,
	}
	url := "http://localhost/upload"
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return fmt.Errorf("got HTTP status %d: %s", res.StatusCode, res.Status)
	}
	return nil
}

func xochitlHash(c *ssh.Client) (string, error) {
	s, err := c.NewSession()
	if err != nil {
		return "", err
	}
	defer s.Close()
	out, err := s.Output("sha256sum /usr/bin/xochitl")
	if err != nil {
		return "", err
	}
	hash := strings.Split(string(out), " ")[0]
	return hash, nil
}

func xochitlBackup(c *ssh.Client) error {
	s, err := c.NewSession()
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Run("cp /usr/bin/xochitl /home/root/xochitl-v2.1.1.3.bak")
}

var xochitl2113WebUIEnabled, _ = base64.RawStdEncoding.DecodeString("" +
	"QlNESUZGNDA5AAAAAAAAAEQAAAAAAAAAnG8/AAAAAABCWmg5MUFZJlNZu1wJDwAACHlgQCAAAgQ" +
	"AAACAAMAAAAQgACGjTCEMCCiAFZvS+LuSKcKEhdrgSHhCWmg5MUFZJlNZJAmYDgAf18ETwAACAE" +
	"AAAKAAAQAIIABQhgBNUozTSqkjmElLv6qIDcVEBubkJAeF3JFOFCQJAmYDgEJaaDkXckU4UJAAAAAA")
var xochitl2113WebUILocalhost, _ = base64.RawStdEncoding.DecodeString("" +
	"QlNESUZGNDA6AAAAAAAAAEoAAAAAAAAAnG8/AAAAAABCWmg5MUFZJlNZ+maq3wAACGtgQCAEAAA" +
	"AgAQAAMAAAAQgACGjR6mhDAh40lYEJ4u5IpwoSH0zVW+AQlpoOTFBWSZTWfqXccwAH9fAMsAAAA" +
	"EQCEAAQo0gAFCAaaaBNUoGjTfyESeYgkWZKVR51VKgLeWtcXuUSA3v8XckU4UJD6l3HMBCWmg5F" +
	"3JFOFCQAAAAAA")

func xochitlPatch(c *ssh.Client) error {
	s, err := c.NewSession()
	if err != nil {
		return err
	}
	defer s.Close()
	old, err := s.Output("cat /usr/bin/xochitl")
	if err != nil {
		return err
	}

	intermediate := &bytes.Buffer{}
	if err := binarydist.Patch(bytes.NewReader(old), intermediate,
		bytes.NewReader(xochitl2113WebUIEnabled)); err != nil {
		return err
	}
	patched := &bytes.Buffer{}
	if err := binarydist.Patch(intermediate, patched,
		bytes.NewReader(xochitl2113WebUILocalhost)); err != nil {
		return err
	}

	// Need to do the overwrite in two steps, or the space made available by
	// truncating the file might be eaten by something before the write is over.
	// Probably some systemd logs.
	s, err = c.NewSession()
	if err != nil {
		return err
	}
	defer s.Close()
	s.Stdin = patched
	return s.Run("cat > /tmp/xochitl && chmod +x /tmp/xochitl && " +
		"mv /tmp/xochitl /usr/bin/xochitl")
}

func xochitlRestart(c *ssh.Client) error {
	s, err := c.NewSession()
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Run("systemctl restart xochitl")
}
