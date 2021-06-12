/*
	A bad gif decoder

	- currently only decodes the first frame
	- doesnt support transparency
	- doesnt support gifs with interlacing
	- pretty much all errors ignored atm ¯\_(ツ)_/¯
	- dependency on compress/lzw, would like to take a stab
	  at implementing Lempel–Ziv–Welch myself for fun and not profit
*/
package main

import (
	"bytes"
	"compress/lzw"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

const (
	extensionIntroducer = 0x21
	imageSeparator      = 0x2C
	graphicControlLabel = 0xF9
	trailer             = 0x3B
	applicationExt      = 0xFF
	commentExt          = 0xFE
)

type image struct {
	leftPosition uint16
	topPosition  uint16
	width        uint16
	height       uint16

	localColorTableFlag bool
	interlaced          bool // (todo) support interlacing
	sortFlag            bool
	localColorTableSize uint16

	pixelData []uint8
	lct       [][]byte
}

type gifdecoder struct {
	// Raw
	databuf [1024]byte
	bytebuf []byte
	r       io.Reader

	// Header Block
	signature string
	version   string

	// Logical Screen Descriptor
	canvasWidth          uint16
	canvasHeight         uint16
	globalColorTableFlag bool
	colorResolution      uint
	sortFlag             bool
	backgroundColorIndex uint8
	pixelAspectRatio     uint8

	// Global Color Table
	gctByteLen uint16
	gct        [][]byte

	// Extension Data
	comments string

	// Image Data
	images []*image
}

func (gd *gifdecoder) decode(r io.Reader) error {
	io.ReadFull(r, gd.databuf[:13])

	gd.signature = string(gd.databuf[:3])

	if gd.signature != "GIF" {
		log.Fatalln("err: a valid gif must be provided as an argument")
		log.Fatalln("usage: gifdecoder.exe path/to/file.gif")
	}
	gd.version = string(gd.databuf[3:6])

	gd.canvasWidth = binary.LittleEndian.Uint16(gd.databuf[6:8])
	gd.canvasHeight = binary.LittleEndian.Uint16(gd.databuf[8:10])
	gd.globalColorTableFlag = false
	if byte(gd.databuf[10])&0b10000000 != 0 {
		gd.globalColorTableFlag = true
	}
	gd.colorResolution = 1 << (1 + uint8(gd.databuf[10]&0b00000111))
	if byte(gd.databuf[10])&0b00001000 != 0 {
		gd.sortFlag = true
	}
	gd.backgroundColorIndex = uint8(gd.databuf[11])
	gd.pixelAspectRatio = uint8(gd.databuf[12])

	if gd.globalColorTableFlag {
		gd.gctByteLen = uint16(3 * (gd.colorResolution))

		gd.gct = make([][]byte, gd.colorResolution)

		io.ReadFull(r, gd.databuf[:gd.gctByteLen])

		i := 0
		for j := range gd.gct {
			gd.gct[j] = make([]byte, 3)
			gd.gct[j] = []byte{gd.databuf[i+0], gd.databuf[i+1], gd.databuf[i+2]}
			i += 3
		}
	}

	gd.bytebuf = make([]byte, 1)
	gd.r = r

	for {
		r.Read(gd.bytebuf)

		switch gd.bytebuf[0] {
		case extensionIntroducer:
			err := gd.readExtension()
			if err != nil {
				return err
			}
		case imageSeparator:
			gd.readImageDescriptor()
		case trailer:
			return nil
		default:
			return fmt.Errorf("unsupported block: 0x%.2x", gd.bytebuf[0])
		}
	}
}

func (gd *gifdecoder) readExtension() error {
	gd.r.Read(gd.bytebuf)

	switch gd.bytebuf[0] {
	case graphicControlLabel:
		gd.readGCE()
	case applicationExt:
		gd.readApplicationExt()
	case commentExt:
		gd.readCommentExt()
	default:
		return fmt.Errorf("unsupported extension: %.2x", gd.bytebuf[0])
	}
	return nil
}

func (gd *gifdecoder) readGCE() error {
	// Skip over basically all the graphic control extension bytes (todo)
	// Not 100% on significance of Byte Size here? (Do GCE vary in length??)
	io.ReadFull(gd.r, gd.databuf[:6])
	return nil
}

func (gd *gifdecoder) readApplicationExt() error {
	// (todo) animation data support, we're just skipping for now

	gd.r.Read(gd.bytebuf)
	blockLen := gd.bytebuf[0]
	io.ReadFull(gd.r, gd.databuf[:blockLen]) // skip application block

	// now skip sub-block
	for {
		gd.r.Read(gd.bytebuf)
		blockLen := gd.bytebuf[0]

		if blockLen == 0 {
			break
		}

		io.ReadFull(gd.r, gd.databuf[:blockLen])
	}

	return nil
}

func (gd *gifdecoder) readCommentExt() error {
	// (todo) malformed gif without a 0x00 sub-block terminator inf loop?
	for {
		gd.r.Read(gd.bytebuf)
		blockLen := gd.bytebuf[0]

		if blockLen == 0 {
			break
		}

		io.ReadFull(gd.r, gd.databuf[:blockLen])
		gd.comments += string(gd.databuf[:blockLen])
		fmt.Println("finished reading comments:", gd.comments)
	}
	return nil
}

func (gd *gifdecoder) readImageDescriptor() error {
	io.ReadFull(gd.r, gd.databuf[:9])
	img := &image{}

	img.leftPosition = binary.LittleEndian.Uint16(gd.databuf[0:2])
	img.topPosition = binary.LittleEndian.Uint16(gd.databuf[2:4])
	img.width = binary.LittleEndian.Uint16(gd.databuf[4:6])
	img.height = binary.LittleEndian.Uint16(gd.databuf[6:8])

	if gd.databuf[8]&0b10000000 != 0 {
		img.localColorTableFlag = true
		img.localColorTableSize = 1 << (1 + uint(gd.databuf[8]&0b00000111))
	}
	if gd.databuf[8]&0b01000000 != 0 {
		img.interlaced = true
	}
	if gd.databuf[8]&0b00100000 != 0 {
		img.sortFlag = true
	}

	//  local color table support
	if img.localColorTableFlag {
		lctByteLen := img.localColorTableSize * 3
		io.ReadFull(gd.r, gd.databuf[:lctByteLen])

		img.lct = make([][]byte, img.localColorTableSize)

		i := 0
		for j := range img.lct {
			img.lct[j] = make([]byte, 3)
			img.lct[j] = []byte{gd.databuf[i+0], gd.databuf[i+1], gd.databuf[i+2]}
			i += 3
		}
	}

	gd.r.Read(gd.bytebuf)
	lzwMinCodeSize := int(gd.bytebuf[0])

	lzwData := []byte{}

	// (todo) malformed gif without a 0x00 sub-block terminator =inf loop?
	for {
		gd.r.Read(gd.bytebuf)
		blockLen := gd.bytebuf[0]

		if blockLen == 0 {
			break
		}

		io.ReadFull(gd.r, gd.databuf[:blockLen])
		lzwData = append(lzwData, gd.databuf[:blockLen]...)
	}

	bb := bytes.NewReader(lzwData)
	lzwReader := lzw.NewReader(bb, lzw.LSB, lzwMinCodeSize)
	defer lzwReader.Close()
	img.pixelData, _ = io.ReadAll(lzwReader)

	gd.images = append(gd.images, img)
	return nil
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	img := gd.images[0]

	fmt.Fprintf(w, `<!doctype html><body><h6>gif->svg</h6><hr><br><svg width="%d" height="%d">`, img.width, img.height)

	x := 0
	y := 0

	for i, p := range img.pixelData {
		x = i % int(img.width)
		y = (i / int(img.width)) % int(img.height)
		r := gd.gct[p][0]
		g := gd.gct[p][1]
		b := gd.gct[p][2]

		if img.localColorTableFlag {
			r = img.lct[p][0]
			g = img.lct[p][1]
			b = img.lct[p][2]
		}

		fmt.Fprintf(w,
			`<rect x="%d" y="%d" width="1" height="1" style="fill:rgb(%d,%d,%d)"/>`,
			x, y, r, g, b)
	}
	fmt.Fprintf(w, `</svg></body></html>`)
}

var gd gifdecoder

func main() {
	//f, err := os.Open("data/nyancat.gif")
	f, err := os.Open(os.Args[1])
	if err != nil {
		log.Fatalln("must specify a valid filename")
	}
	err = gd.decode(f)
	f.Close()

	if err != nil {
		fmt.Println(err)
	}
	fmt.Println("running webserver @ http://127.0.0.1:8000")
	http.HandleFunc("/", indexHandler)
	log.Fatal(http.ListenAndServe("127.0.0.1:8000", nil))
}
