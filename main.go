package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const (
	nbdMagic       uint64 = 0x4e42444d41474943
	ihaveopt       uint64 = 0x49484156454f5054
	replyMagicOpt  uint64 = 0x0003e889045565a9
	reqMagic       uint32 = 0x25609513
	simpleRepMagic uint32 = 0x67446698

	flagFixedNewstyle uint16 = 1 << 0
	flagNoZeroes      uint16 = 1 << 1
	cFlagFixed        uint32 = 1 << 0
	cFlagNoZeroes     uint32 = 1 << 1

	flagHasFlags uint16 = 1 << 0
	flagReadOnly uint16 = 1 << 1

	optExportName uint32 = 1
	optAbort      uint32 = 2
	optList       uint32 = 3
	optInfo       uint32 = 6
	optGo         uint32 = 7

	repAck        uint32 = 1
	repServer     uint32 = 2
	repInfo       uint32 = 3
	repErrUnsup   uint32 = 0x80000001
	repErrInvalid uint32 = 0x80000003

	infoExport    uint16 = 0
	infoName      uint16 = 1
	infoBlockSize uint16 = 3

	cmdRead        uint16 = 0
	cmdWrite       uint16 = 1
	cmdDisc        uint16 = 2
	cmdFlush       uint16 = 3
	cmdTrim        uint16 = 4
	cmdWriteZeroes uint16 = 6

	errEPERM  uint32 = 1
	errEIO    uint32 = 5
	errEINVAL uint32 = 22
	errEROFS  uint32 = 30
)

type Backend interface {
	ReadAt([]byte, int64) (int, error)
	Size() int64
	Close() error
	Description() string
}

type config struct {
	Disk      int
	Partition int
	Offset    int64
	Size      int64
	Listen    string
	ProbeOnly bool
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	var c config
	flag.IntVar(&c.Disk, "disk", -1, "Windows disk number, e.g. 0")
	flag.IntVar(&c.Partition, "partition", -1, "Windows partition number, e.g. 2")
	flag.Int64Var(&c.Offset, "offset", -1, "partition byte offset (optional override)")
	flag.Int64Var(&c.Size, "size", -1, "partition byte size (optional override)")
	flag.StringVar(&c.Listen, "listen", "127.0.0.1:10809", "NBD listen address")
	flag.BoolVar(&c.ProbeOnly, "probe", false, "read Btrfs magic and exit; do not start NBD")
	flag.Parse()

	if c.Disk < 0 {
		fatalf("--disk is required")
	}
	if c.Offset < 0 || c.Size <= 0 {
		if c.Partition < 0 {
			fatalf("--partition is required unless --offset and --size are supplied")
		}
		off, size, err := queryPartition(c.Disk, c.Partition)
		if err != nil {
			fatalf("query partition: %v", err)
		}
		c.Offset, c.Size = off, size
	}
	if c.Offset < 0 || c.Size <= 0 {
		fatalf("invalid offset/size")
	}

	b, err := openWindowsRawBackend(c.Disk, c.Offset, c.Size)
	if err != nil {
		fatalf("open raw disk: %v", err)
	}
	defer b.Close()

	fmt.Printf("wsl-partition-bridge (READ ONLY)\n")
	fmt.Printf("Backend   : %s\n", b.Description())
	fmt.Printf("Offset    : %d bytes\n", c.Offset)
	fmt.Printf("Size      : %d bytes (%.2f GiB)\n", c.Size, float64(c.Size)/(1024*1024*1024))

	magic, err := probeBtrfs(b)
	if err != nil {
		fmt.Printf("Probe     : raw read OK, Btrfs probe error: %v\n", err)
	} else {
		fmt.Printf("Probe     : Btrfs magic [%s]\n", magic)
	}
	if c.ProbeOnly {
		return
	}

	ln, err := net.Listen("tcp", c.Listen)
	if err != nil {
		fatalf("listen %s: %v", c.Listen, err)
	}
	defer ln.Close()
	fmt.Printf("NBD       : nbd://%s (read-only)\n", c.Listen)
	fmt.Printf("Security  : writes/TRIM/zeroes are rejected; Windows handle has no write access\n")
	fmt.Printf("Press Ctrl+C to stop.\n")

	for {
		conn, err := ln.Accept()
		if err != nil {
			fatalf("accept: %v", err)
		}
		go func() {
			defer conn.Close()
			if tc, ok := conn.(*net.TCPConn); ok {
				_ = tc.SetNoDelay(true)
			}
			log.Printf("client connected: %s", conn.RemoteAddr())
			if err := serveNBD(conn, b); err != nil && !errors.Is(err, io.EOF) {
				log.Printf("client %s: %v", conn.RemoteAddr(), err)
			}
			log.Printf("client disconnected: %s", conn.RemoteAddr())
		}()
	}
}

func fatalf(f string, a ...any) { fmt.Fprintf(os.Stderr, "ERROR: "+f+"\n", a...); os.Exit(1) }

func queryPartition(disk, part int) (int64, int64, error) {
	script := fmt.Sprintf("$p=Get-Partition -DiskNumber %d -PartitionNumber %d -ErrorAction Stop; Write-Output ($p.Offset.ToString() + ',' + $p.Size.ToString())", disk, part)
	names := []string{"pwsh.exe", "powershell.exe"}
	var last error
	for _, name := range names {
		out, err := exec.Command(name, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
		if err != nil {
			last = fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
			continue
		}
		fields := strings.Split(strings.TrimSpace(string(out)), ",")
		if len(fields) != 2 {
			last = fmt.Errorf("unexpected Get-Partition output: %q", strings.TrimSpace(string(out)))
			continue
		}
		off, e1 := strconv.ParseInt(strings.TrimSpace(fields[0]), 10, 64)
		size, e2 := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64)
		if e1 != nil || e2 != nil {
			last = fmt.Errorf("parse Get-Partition output: %q", strings.TrimSpace(string(out)))
			continue
		}
		return off, size, nil
	}
	return 0, 0, last
}

func probeBtrfs(b Backend) (string, error) {
	// Primary Btrfs superblock starts at 64 KiB; magic is +0x40.
	buf := make([]byte, 4096)
	if _, err := b.ReadAt(buf, 65536); err != nil {
		return "", err
	}
	magic := string(buf[0x40:0x48])
	if magic != "_BHRfS_M" {
		return magic, fmt.Errorf("expected _BHRfS_M")
	}
	return magic, nil
}

func serveNBD(c net.Conn, b Backend) error {
	// Fixed newstyle, no-zeroes capable.
	if err := writeBE(c, nbdMagic, ihaveopt, uint16(flagFixedNewstyle|flagNoZeroes)); err != nil {
		return err
	}
	var clientFlags uint32
	if err := binary.Read(c, binary.BigEndian, &clientFlags); err != nil {
		return err
	}
	if clientFlags & ^uint32(cFlagFixed|cFlagNoZeroes) != 0 {
		return fmt.Errorf("unsupported client flags 0x%x", clientFlags)
	}
	noZeroes := clientFlags&cFlagNoZeroes != 0

	for {
		var magic uint64
		var opt, length uint32
		if err := binary.Read(c, binary.BigEndian, &magic); err != nil {
			return err
		}
		if magic != ihaveopt {
			return fmt.Errorf("bad option magic 0x%x", magic)
		}
		if err := binary.Read(c, binary.BigEndian, &opt); err != nil {
			return err
		}
		if err := binary.Read(c, binary.BigEndian, &length); err != nil {
			return err
		}
		if length > 1<<20 {
			return fmt.Errorf("option payload too large: %d", length)
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(c, payload); err != nil {
			return err
		}

		switch opt {
		case optExportName:
			if err := writeBE(c, uint64(b.Size()), uint16(flagHasFlags|flagReadOnly)); err != nil {
				return err
			}
			if !noZeroes {
				if _, err := c.Write(make([]byte, 124)); err != nil {
					return err
				}
			}
			return transmission(c, b)
		case optInfo, optGo:
			name, reqInfos, err := parseInfoGo(payload)
			if err != nil {
				if e := sendOptReply(c, opt, repErrInvalid, nil); e != nil {
					return e
				}
				continue
			}
			_ = name // one default export; any name maps to it for convenience
			// NBD_INFO_EXPORT is mandatory before ACK.
			data := make([]byte, 12)
			binary.BigEndian.PutUint16(data[0:2], infoExport)
			binary.BigEndian.PutUint64(data[2:10], uint64(b.Size()))
			binary.BigEndian.PutUint16(data[10:12], flagHasFlags|flagReadOnly)
			if err := sendOptReply(c, opt, repInfo, data); err != nil {
				return err
			}
			for _, info := range reqInfos {
				switch info {
				case infoName:
					d := append([]byte{0, byte(infoName)}, []byte("default")...)
					if err := sendOptReply(c, opt, repInfo, d); err != nil {
						return err
					}
				case infoBlockSize:
					d := make([]byte, 14)
					binary.BigEndian.PutUint16(d[0:2], infoBlockSize)
					binary.BigEndian.PutUint32(d[2:6], 512)      // minimum
					binary.BigEndian.PutUint32(d[6:10], 4096)    // preferred
					binary.BigEndian.PutUint32(d[10:14], 32<<20) // maximum
					if err := sendOptReply(c, opt, repInfo, d); err != nil {
						return err
					}
				}
			}
			if err := sendOptReply(c, opt, repAck, nil); err != nil {
				return err
			}
			if opt == optGo {
				return transmission(c, b)
			}
		case optList:
			name := []byte("default")
			d := make([]byte, 4+len(name))
			binary.BigEndian.PutUint32(d[0:4], uint32(len(name)))
			copy(d[4:], name)
			if err := sendOptReply(c, opt, repServer, d); err != nil {
				return err
			}
			if err := sendOptReply(c, opt, repAck, nil); err != nil {
				return err
			}
		case optAbort:
			_ = sendOptReply(c, opt, repAck, nil)
			return nil
		default:
			if err := sendOptReply(c, opt, repErrUnsup, nil); err != nil {
				return err
			}
		}
	}
}

func parseInfoGo(p []byte) (string, []uint16, error) {
	if len(p) < 6 {
		return "", nil, errors.New("short INFO/GO")
	}
	nlen := int(binary.BigEndian.Uint32(p[0:4]))
	if nlen < 0 || 4+nlen+2 > len(p) {
		return "", nil, errors.New("bad name length")
	}
	name := string(p[4 : 4+nlen])
	pos := 4 + nlen
	nreq := int(binary.BigEndian.Uint16(p[pos : pos+2]))
	pos += 2
	if pos+nreq*2 != len(p) {
		return "", nil, errors.New("bad info list")
	}
	infos := make([]uint16, nreq)
	for i := range infos {
		infos[i] = binary.BigEndian.Uint16(p[pos+i*2 : pos+i*2+2])
	}
	return name, infos, nil
}

func sendOptReply(w io.Writer, opt, typ uint32, data []byte) error {
	if err := writeBE(w, replyMagicOpt, opt, typ, uint32(len(data))); err != nil {
		return err
	}
	if len(data) > 0 {
		_, err := w.Write(data)
		return err
	}
	return nil
}

func transmission(c net.Conn, b Backend) error {
	hdr := make([]byte, 28)
	for {
		if _, err := io.ReadFull(c, hdr); err != nil {
			return err
		}
		if binary.BigEndian.Uint32(hdr[0:4]) != reqMagic {
			return fmt.Errorf("bad request magic")
		}
		flags := binary.BigEndian.Uint16(hdr[4:6])
		typ := binary.BigEndian.Uint16(hdr[6:8])
		cookie := binary.BigEndian.Uint64(hdr[8:16])
		off := binary.BigEndian.Uint64(hdr[16:24])
		length := binary.BigEndian.Uint32(hdr[24:28])
		_ = flags

		if typ == cmdDisc {
			return nil
		}
		if off > uint64(b.Size()) || uint64(length) > uint64(b.Size())-off {
			if typ == cmdWrite {
				if _, err := io.CopyN(io.Discard, c, int64(length)); err != nil {
					return err
				}
			}
			if err := simpleReply(c, errEINVAL, cookie, nil); err != nil {
				return err
			}
			continue
		}
		if length > 32<<20 {
			if typ == cmdWrite {
				if _, err := io.CopyN(io.Discard, c, int64(length)); err != nil {
					return err
				}
			}
			if err := simpleReply(c, errEINVAL, cookie, nil); err != nil {
				return err
			}
			continue
		}

		switch typ {
		case cmdRead:
			data := make([]byte, int(length))
			n, err := b.ReadAt(data, int64(off))
			if err != nil && !errors.Is(err, io.EOF) {
				if e := simpleReply(c, errEIO, cookie, nil); e != nil {
					return e
				}
				continue
			}
			if n != len(data) {
				if e := simpleReply(c, errEIO, cookie, nil); e != nil {
					return e
				}
				continue
			}
			if err := simpleReply(c, 0, cookie, data); err != nil {
				return err
			}
		case cmdWrite:
			// Consume payload, but NEVER write it anywhere.
			if _, err := io.CopyN(io.Discard, c, int64(length)); err != nil {
				return err
			}
			if err := simpleReply(c, errEROFS, cookie, nil); err != nil {
				return err
			}
		case cmdFlush, cmdTrim, cmdWriteZeroes:
			if err := simpleReply(c, errEROFS, cookie, nil); err != nil {
				return err
			}
		default:
			if err := simpleReply(c, errEPERM, cookie, nil); err != nil {
				return err
			}
		}
	}
}

func simpleReply(w io.Writer, errno uint32, cookie uint64, data []byte) error {
	if err := writeBE(w, simpleRepMagic, errno, cookie); err != nil {
		return err
	}
	if len(data) > 0 {
		_, err := w.Write(data)
		return err
	}
	return nil
}

func writeBE(w io.Writer, vals ...any) error {
	for _, v := range vals {
		if err := binary.Write(w, binary.BigEndian, v); err != nil {
			return err
		}
	}
	return nil
}
