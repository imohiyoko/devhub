package ports

import "testing"

func TestParseLsof(t *testing.T) {
	// Header + a normal row, an escaped-space command, an IPv6 row, and a junk row.
	out := "COMMAND   PID  USER   FD   TYPE   DEVICE SIZE/OFF NODE NAME\n" +
		"node    16727  alice  23u  IPv4 0x1234      0t0  TCP *:3000 (LISTEN)\n" +
		"Google\\x20Chrome 999 alice 50u IPv4 0xabcd 0t0 TCP 127.0.0.1:8080 (LISTEN)\n" +
		"sshd      55  root   3u   IPv6 0x9999      0t0  TCP [::1]:22 (LISTEN)\n" +
		"garbage line without listen\n"
	got := parseLsof(out)
	if len(got) != 3 {
		t.Fatalf("parseLsof returned %d rows, want 3: %+v", len(got), got)
	}
	if got[0].command != "node" || got[0].pid != 16727 || got[0].port != 3000 || got[0].host != "*" {
		t.Errorf("row0 = %+v", got[0])
	}
	if got[1].command != "Google Chrome" || got[1].port != 8080 || got[1].host != "127.0.0.1" {
		t.Errorf("row1 (escaped space / host) = %+v", got[1])
	}
	if got[2].host != "[::1]" || got[2].port != 22 {
		t.Errorf("row2 (ipv6) = %+v", got[2])
	}
}

func TestParseNetstat(t *testing.T) {
	out := "\nActive Connections\n\n  Proto  Local Address          Foreign Address        State           PID\n" +
		"  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1044\n" +
		"  TCP    127.0.0.1:5354         0.0.0.0:0              ESTABLISHED     2222\n" +
		"  TCP    [::]:445               [::]:0                 LISTENING       4\n"
	got := parseNetstat(out)
	if len(got) != 2 {
		t.Fatalf("parseNetstat returned %d rows, want 2: %+v", len(got), got)
	}
	if got[0].port != 135 || got[0].pid != 1044 || got[0].host != "0.0.0.0" {
		t.Errorf("row0 = %+v", got[0])
	}
	if got[1].port != 445 || got[1].pid != 4 || got[1].host != "[::]" {
		t.Errorf("row1 (ipv6) = %+v", got[1])
	}
}
