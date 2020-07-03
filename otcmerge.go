// Copyright 2020 Nik Unger
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package otcmerge combines TrueType / OpenType font files (.ttf / .otf) into
// TrueType / OpenType Collection files (.ttc / .otc).
package otcmerge

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

type merger struct {
	w           io.WriteSeeker
	tables      []*tableRecord
	fontOffsets []uint32
}

type tableRecord struct {
	off       int64
	tag       uint32
	sfntSum   uint32
	sz        uint32
	cryptoSum []byte
}

// Option specifies additional configuration for Merge. No options are currently available, but the interface exists so
// that future changes are backward compatible.
type Option interface {
	unexported()
}

// Merge combines the given fonts (in SFNT format) into a single OpenType Collection file. The input fonts must be
// individual TrueType / OpenType files; collections are not supported (and so this function cannot be used
// recursively). This merger is lossless. Merge will de-duplicate SFNT tables that are bit-for-bit matches, but it does
// not perform any other merging. Specifically, Merge does not parse the contents of SFNT tables.
func Merge(fonts []io.ReadSeeker, out io.WriteSeeker, options ...Option) error {
	m := &merger{w: out}
	if err := m.writeHeader(uint32(len(fonts))); err != nil {
		return fmt.Errorf("failed to write font collection header: %w", err)
	}
	for i, f := range fonts {
		if err := m.copySFNT(f); err != nil {
			return fmt.Errorf("failed to copy SFNT data from font %d: %w", i, err)
		}
	}
	if err := m.patchOffsetTables(); err != nil {
		return fmt.Errorf("failed to write font offset tables: %w", err)
	}
	return nil
}

func (m *merger) writeHeader(numFonts uint32) error {
	if _, err := m.w.Write([]byte("ttcf\x00\x01\x00\x00")); err != nil {
		return err
	}
	if err := binary.Write(m.w, binary.BigEndian, numFonts); err != nil {
		return err
	}
	// Fill offset table with placeholders so that we can copy each font in a single pass
	for i := uint32(0); i < numFonts; i++ {
		if _, err := m.w.Write([]byte{0xff, 0xff, 0xff, 0xff}); err != nil {
			return err
		}
	}
	return nil
}

func (m *merger) patchOffsetTables() error {
	if _, err := m.w.Seek(4+2+2+4, io.SeekStart); err != nil {
		return err
	}
	for _, off := range m.fontOffsets {
		if err := binary.Write(m.w, binary.BigEndian, off); err != nil {
			return err
		}
	}
	return nil
}

func (m *merger) copySFNT(f io.ReadSeeker) error {
	var headerBuf [4 + 2]byte
	if _, err := io.ReadFull(f, headerBuf[:]); err != nil {
		return err
	}
	switch string(headerBuf[:4]) {
	case "\x00\x01\x00\x00":
	case "OTTO":
	default:
		return errors.New("unsupported SFNT version")
	}
	numTables := binary.BigEndian.Uint16(headerBuf[4:6])
	if numTables < 1 {
		return errors.New("SFNT file does not contain any data")
	}

	fontOffset, err := m.w.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	m.fontOffsets = append(m.fontOffsets, uint32(fontOffset))
	if _, err := m.w.Write(headerBuf[:]); err != nil {
		return err
	}

	// Correct the binary search values by recalculating them
	entrySelector := uint16(math.Log2(float64(numTables))) // float64 perfectly represents uint16 values
	searchRange := uint16(1<<entrySelector) * 16
	rangeShift := numTables*16 - searchRange
	if err := binary.Write(m.w, binary.BigEndian, searchRange); err != nil {
		return err
	}
	if err := binary.Write(m.w, binary.BigEndian, entrySelector); err != nil {
		return err
	}
	if err := binary.Write(m.w, binary.BigEndian, rangeShift); err != nil {
		return err
	}

	// Skip to the table records
	if _, err := f.Seek(2+2+2, io.SeekCurrent); err != nil {
		return err
	}

	// Fill in placeholders for table entries to patch in later
	tableStart := fontOffset + 4 + 2 + 2 + 2 + 2
	for i := uint16(0); i < numTables; i++ {
		if _, err := m.w.Write([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}); err != nil {
			return err
		}
	}

	for i := uint16(0); i < numTables; i++ {
		tblEntryOffset := tableStart + int64(i)*(4+4+4+4)
		if err := m.copyTable(tblEntryOffset, f); err != nil {
			return fmt.Errorf("failed to copy SFNT table %d: %w", i, err)
		}
	}

	return nil
}

func (m *merger) copyTable(tblEntryOffset int64, f io.ReadSeeker) error {
	var recordBuf [4 + 4 + 4 + 4]byte
	if _, err := io.ReadFull(f, recordBuf[:]); err != nil {
		return err
	}
	tag := binary.BigEndian.Uint32(recordBuf[:4])
	sfntSum := binary.BigEndian.Uint32(recordBuf[4:8])
	tblOffset := binary.BigEndian.Uint32(recordBuf[8:12])
	sz := binary.BigEndian.Uint32(recordBuf[12:])

	nextEntryOff, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}

	// Merge duplicate tables between SFNT files to save space, if possible
	writtenOffset, duplicate, err := m.lookupTable(tag, sfntSum, sz, f, tblOffset)
	if err != nil {
		return err
	}
	if !duplicate {
		if _, err := f.Seek(int64(tblOffset), io.SeekStart); err != nil {
			return err
		}
		writtenOffset, err = m.w.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		h := sha256.New()
		w := io.MultiWriter(m.w, h)
		if _, err := io.CopyN(w, f, int64(sz)); err != nil {
			return err
		}
		// Ensure that all tables begin on 32-bit boundaries by inserting padding (all headers are already aligned)
		if sz%4 != 0 {
			if _, err := m.w.Write(make([]byte, 4-sz%4)); err != nil {
				return err
			}
		}
		m.recordTable(writtenOffset, tag, sfntSum, sz, h.Sum(nil))
	}

	// Patch the table entry in the output with the actual written offset
	if _, err := m.w.Seek(tblEntryOffset, io.SeekStart); err != nil {
		return err
	}
	if _, err := m.w.Write(recordBuf[:8]); err != nil {
		return err
	}
	if err := binary.Write(m.w, binary.BigEndian, uint32(writtenOffset)); err != nil {
		return err
	}
	if _, err := m.w.Write(recordBuf[12:]); err != nil {
		return err
	}
	if _, err := m.w.Seek(0, io.SeekEnd); err != nil {
		return err
	}

	// Move to read the next table entry
	if _, err := f.Seek(nextEntryOff, io.SeekStart); err != nil {
		return err
	}
	return nil
}

func (m *merger) recordTable(writtenOffset int64, tag uint32, sfntSum uint32, sz uint32, cryptoSum []byte) {
	m.tables = append(m.tables, &tableRecord{
		off:       writtenOffset,
		tag:       tag,
		sfntSum:   sfntSum,
		sz:        sz,
		cryptoSum: cryptoSum,
	})
}

func (m *merger) lookupTable(tag uint32, sfntSum uint32, sz uint32, f io.ReadSeeker, readOff uint32) (int64, bool, error) {
	for _, tbl := range m.tables {
		if tbl.tag != tag {
			continue
		}
		if tbl.sz != sz {
			continue
		}
		if tbl.sfntSum != sfntSum {
			continue
		}

		// Cryptographic hash comparison requires a dual pass over the source table, so only do it if everything else
		// matches. An alternative would be to always copy the data to the output while hashing, and then truncate the
		// file to remove duplicate tables. This approach is superior if reading is faster/preferred over writing.
		if _, err := f.Seek(int64(readOff), io.SeekStart); err != nil {
			return 0, false, err
		}
		h := sha256.New()
		if _, err := io.CopyN(h, f, int64(sz)); err != nil {
			return 0, false, err
		}
		if !bytes.Equal(tbl.cryptoSum, h.Sum(nil)) {
			continue
		}
		return tbl.off, true, nil
	}
	return 0, false, nil
}
