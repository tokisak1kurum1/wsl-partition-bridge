package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
)

type memBackend struct{ b []byte }

func (m *memBackend) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > int64(len(m.b)) {
		return 0, io.EOF
	}
	copy(p, m.b[off:])
	return len(p), nil
}
func (m *memBackend) Size() int64         { return int64(len(m.b)) }
func (m *memBackend) Close() error        { return nil }
func (m *memBackend) Description() string { return "mem" }

func TestExportNameReadAndWriteReject(t *testing.T) {
	mb := &memBackend{b: make([]byte, 1<<20)}
	copy(mb.b[4096:], []byte("hello-nbd"))
	srv, cli := net.Pipe()
	defer cli.Close()
	done := make(chan error, 1)
	go func() { done <- serveNBD(srv, mb); srv.Close() }()

	var u64 uint64
	var hs uint16
	binary.Read(cli, binary.BigEndian, &u64)
	if u64 != nbdMagic {
		t.Fatal(u64)
	}
	binary.Read(cli, binary.BigEndian, &u64)
	if u64 != ihaveopt {
		t.Fatal(u64)
	}
	binary.Read(cli, binary.BigEndian, &hs)
	binary.Write(cli, binary.BigEndian, uint32(cFlagFixed|cFlagNoZeroes))
	writeBE(cli, ihaveopt, optExportName, uint32(0))
	binary.Read(cli, binary.BigEndian, &u64)
	if int64(u64) != mb.Size() {
		t.Fatalf("size %d", u64)
	}
	binary.Read(cli, binary.BigEndian, &hs)
	if hs&flagReadOnly == 0 {
		t.Fatalf("not readonly: %x", hs)
	}

	cookie := uint64(123)
	req := make([]byte, 28)
	binary.BigEndian.PutUint32(req[0:4], reqMagic)
	binary.BigEndian.PutUint16(req[6:8], cmdRead)
	binary.BigEndian.PutUint64(req[8:16], cookie)
	binary.BigEndian.PutUint64(req[16:24], 4096)
	binary.BigEndian.PutUint32(req[24:28], 9)
	cli.Write(req)
	rep := make([]byte, 16)
	io.ReadFull(cli, rep)
	if binary.BigEndian.Uint32(rep[:4]) != simpleRepMagic || binary.BigEndian.Uint32(rep[4:8]) != 0 {
		t.Fatalf("bad reply %x", rep)
	}
	data := make([]byte, 9)
	io.ReadFull(cli, data)
	if !bytes.Equal(data, []byte("hello-nbd")) {
		t.Fatalf("%q", data)
	}

	binary.BigEndian.PutUint16(req[6:8], cmdWrite)
	binary.BigEndian.PutUint64(req[16:24], 8192)
	binary.BigEndian.PutUint32(req[24:28], 4)
	cli.Write(req)
	cli.Write([]byte("EVIL"))
	io.ReadFull(cli, rep)
	if binary.BigEndian.Uint32(rep[4:8]) != errEROFS {
		t.Fatalf("write errno=%d", binary.BigEndian.Uint32(rep[4:8]))
	}

	binary.BigEndian.PutUint16(req[6:8], cmdDisc)
	binary.BigEndian.PutUint32(req[24:28], 0)
	cli.Write(req)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGoNegotiation(t *testing.T) {
	mb := &memBackend{b: make([]byte, 2<<20)}
	srv, cli := net.Pipe()
	defer cli.Close()
	done := make(chan error, 1)
	go func() { done <- serveNBD(srv, mb); srv.Close() }()
	var u64 uint64
	var hs uint16
	binary.Read(cli, binary.BigEndian, &u64)
	binary.Read(cli, binary.BigEndian, &u64)
	binary.Read(cli, binary.BigEndian, &hs)
	binary.Write(cli, binary.BigEndian, uint32(cFlagFixed|cFlagNoZeroes))

	name := []byte("default")
	payload := make([]byte, 4+len(name)+2+2)
	binary.BigEndian.PutUint32(payload[0:4], uint32(len(name)))
	copy(payload[4:], name)
	pos := 4 + len(name)
	binary.BigEndian.PutUint16(payload[pos:pos+2], 1)
	pos += 2
	binary.BigEndian.PutUint16(payload[pos:pos+2], infoBlockSize)
	writeBE(cli, ihaveopt, optGo, uint32(len(payload)))
	cli.Write(payload)

	// First reply: INFO_EXPORT
	var rm uint64
	var opt, rt, l uint32
	binary.Read(cli, binary.BigEndian, &rm)
	binary.Read(cli, binary.BigEndian, &opt)
	binary.Read(cli, binary.BigEndian, &rt)
	binary.Read(cli, binary.BigEndian, &l)
	if rm != replyMagicOpt || opt != optGo || rt != repInfo || l != 12 {
		t.Fatalf("bad export info hdr")
	}
	d := make([]byte, l)
	io.ReadFull(cli, d)
	if binary.BigEndian.Uint16(d[:2]) != infoExport {
		t.Fatal("missing export info")
	}
	// Second reply: requested block size
	binary.Read(cli, binary.BigEndian, &rm)
	binary.Read(cli, binary.BigEndian, &opt)
	binary.Read(cli, binary.BigEndian, &rt)
	binary.Read(cli, binary.BigEndian, &l)
	d = make([]byte, l)
	io.ReadFull(cli, d)
	if binary.BigEndian.Uint16(d[:2]) != infoBlockSize {
		t.Fatal("missing block size")
	}
	// ACK
	binary.Read(cli, binary.BigEndian, &rm)
	binary.Read(cli, binary.BigEndian, &opt)
	binary.Read(cli, binary.BigEndian, &rt)
	binary.Read(cli, binary.BigEndian, &l)
	if rt != repAck || l != 0 {
		t.Fatalf("no ack")
	}

	req := make([]byte, 28)
	binary.BigEndian.PutUint32(req[0:4], reqMagic)
	binary.BigEndian.PutUint16(req[6:8], cmdDisc)
	cli.Write(req)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
