package mailer

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

// Only an in-memory pipe: exercises SMTP's actual DATA acknowledgment boundary
// without any external server, credentials, TLS exception or production mail.
func TestDeliveryAcceptanceDoesNotDependOnQuit(t *testing.T) {
	for _, mode := range []string{"quit-ok", "quit-reject", "quit-disconnect", "data-reject", "data-unknown"} {
		t.Run(mode, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			client.SetDeadline(time.Now().Add(2 * time.Second))
			server.SetDeadline(time.Now().Add(2 * time.Second))
			done := make(chan error, 1)
			go func() {
				defer server.Close()
				r := bufio.NewReader(server)
				reply := func(line string) error { _, err := fmt.Fprint(server, line+"\r\n"); return err }
				if err := reply("220 localhost SMTP"); err != nil {
					done <- err
					return
				}
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						done <- err
						return
					}
					cmd := strings.TrimSpace(line)
					switch {
					case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"), strings.HasPrefix(cmd, "MAIL FROM:"), strings.HasPrefix(cmd, "RCPT TO:"):
						err = reply("250 OK")
					case cmd == "DATA":
						if err = reply("354 data"); err != nil {
							done <- err
							return
						}
						for {
							line, err = r.ReadString('\n')
							if err != nil {
								done <- err
								return
							}
							if line == ".\r\n" {
								break
							}
						}
						if mode == "data-unknown" {
							done <- nil
							return
						}
						if mode == "data-reject" {
							err = reply("550 rejected")
							done <- err
							return
						}
						err = reply("250 queued")
					case cmd == "QUIT":
						if mode == "quit-disconnect" {
							done <- nil
							return
						}
						if mode == "quit-reject" {
							err = reply("500 quit unsupported")
						} else {
							err = reply("221 bye")
						}
						done <- err
						return
					default:
						done <- fmt.Errorf("unexpected command %q", cmd)
						return
					}
					if err != nil {
						done <- err
						return
					}
				}
			}()
			c, err := smtp.NewClient(client, "localhost")
			if err != nil {
				t.Fatal(err)
			}
			err = deliver(c, nil, "sender@example.test", []string{"recipient@example.test"}, []byte("Subject: Test\r\n\r\nBody\r\n"))
			switch mode {
			case "data-reject":
				if err == nil || errors.Is(err, ErrDeliveryUncertain) {
					t.Fatalf("definite rejection=%v", err)
				}
			case "data-unknown":
				if !errors.Is(err, ErrDeliveryUncertain) {
					t.Fatalf("uncertain DATA=%v", err)
				}
			default:
				if err != nil {
					t.Fatalf("accepted DATA falsely failed: %v", err)
				}
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("SMTP fixture did not finish")
			}
		})
	}
}
