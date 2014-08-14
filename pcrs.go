// Copyright (c) 2014, Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tpm

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/golang/glog"
)

// A pcrValue is the fixed-size value of a PCR.
type pcrValue [20]byte

// Each PCR has a fixed size of 20 bytes.
const PCRSize int = 20

// A pcrMask represents a set of PCR choices, one bit per PCR out of the 24
// possible PCR values.
type pcrMask [3]byte

// setPCR sets a PCR value as selected in a given mask.
func (pm *pcrMask) setPCR(i int) error {
	if i >= 24 || i < 0 {
		return errors.New("can't set PCR " + strconv.Itoa(i))
	}

	(*pm)[i/8] |= 1 << uint(i%8)
	return nil
}

// isPCRSet checks to see if a given PCR is included in this mask.
func (pm pcrMask) isPCRSet(i int) (bool, error) {
	if i >= 24 || i < 0 {
		return false, errors.New("can't check PCR " + strconv.Itoa(i))
	}

	n := byte(1 << uint(i%8))
	return pm[i/8]&n == n, nil
}

// A pcrSelection is the first element in the input a PCR composition, which is
// A pcrSelection, followed by the combined length of the PCR values,
// followed by the PCR values, all hashed under SHA-1.
type pcrSelection struct {
	Size uint16
	Mask pcrMask
}

// String returns a string representation of a pcrSelection
func (p pcrSelection) String() string {
	return fmt.Sprintf("pcrSelection{Size: %x, Mask: % x}", p.Size, p.Mask)
}

// newPCRSelection creates a new pcrSelection for the given set of PCRs.
func newPCRSelection(pcrVals []int) (*pcrSelection, error) {
	pcrs := &pcrSelection{Size: 3}
	for _, v := range pcrVals {
		if err := pcrs.Mask.setPCR(v); err != nil {
			return nil, err
		}
	}

	return pcrs, nil
}

// createPCRComposite composes a set of PCRs by prepending a pcrSelection and a
// length, then computing the SHA1 hash and returning its output.
func createPCRComposite(mask pcrMask, pcrs []byte) ([]byte, error) {
	if len(pcrs)%PCRSize != 0 {
		return nil, errors.New("pcrs must be a multiple of " + strconv.Itoa(PCRSize))
	}

	in := []interface{}{pcrSelection{3, mask}, pcrs}
	b, err := pack(in)
	if err != nil {
		return nil, err
	}
	if glog.V(2) {
		glog.Infof("composite buffer for mask %s is % x\n", mask, b)
	}

	h := sha1.Sum(b)
	if glog.V(2) {
		glog.Infof("SHA1 hash of composite buffer is % x\n", h)
	}

	return h[:], nil
}

// pcrInfoLong stores detailed information about PCRs.
type pcrInfoLong struct {
	Tag              uint16
	LocAtCreation    byte
	LocAtRelease     byte
	PCRsAtCreation   pcrSelection
	PCRsAtRelease    pcrSelection
	DigestAtCreation digest
	DigestAtRelease  digest
}

// String returns a string representation of a pcrInfoLong.
func (pcri pcrInfoLong) String() string {
	return fmt.Sprintf("pcrInfoLong{Tag: %x, LocAtCreation: %x, LocAtRelease: %x, PCRsAtCreation: %s, PCRsAtRelease: %s, DigestAtCreation: % x, DigestAtRelease: % x}", pcri.Tag, pcri.LocAtCreation, pcri.LocAtRelease, pcri.PCRsAtCreation, pcri.PCRsAtRelease, pcri.DigestAtCreation, pcri.DigestAtRelease)
}

// pcrInfoShort stores detailed information about PCRs.
type pcrInfoShort struct {
	LocAtRelease    byte
	PCRsAtRelease   pcrSelection
	DigestAtRelease digest
}

// String returns a string representation of a pcrInfoShort.
func (pcri pcrInfoShort) String() string {
	return fmt.Sprintf("pcrInfoShort{LocAtRelease: %x, PCRsAtRelease: %s, DigestAtRelease: % x}", pcri.LocAtRelease, pcri.PCRsAtRelease, pcri.DigestAtRelease)
}

// A capVersionInfo contains information about the TPM itself. Note that this
// is deserialized specially, since it has a variable-length byte array but no
// length. It is preceeded with a length in the response to the Quote2 command.
type capVersionInfo struct {
	CapVersionFixed capVersionInfoFixed
	VendorSpecific  []byte
}

// A capVersionInfoFixed stores the fixed-length part of capVersionInfo.
type capVersionInfoFixed struct {
	Tag       uint16
	Version   uint32
	SpecLevel uint16
	ErrataRev byte
	VendorID  byte
}

// createPCRInfoLong creates a pcrInfoLong structure from a mask and some PCR
// values that match this mask, along with a TPM locality.
func createPCRInfoLong(loc byte, mask pcrMask, pcrVals []byte) (*pcrInfoLong, error) {
	d, err := createPCRComposite(mask, pcrVals)
	if err != nil {
		return nil, err
	}

	locVal := byte(1 << loc)
	pcri := &pcrInfoLong{
		Tag:            tagPCRInfoLong,
		LocAtCreation:  locVal,
		LocAtRelease:   locVal,
		PCRsAtCreation: pcrSelection{3, mask},
		PCRsAtRelease:  pcrSelection{3, mask},
	}

	copy(pcri.DigestAtRelease[:], d)
	copy(pcri.DigestAtCreation[:], d)

	if glog.V(2) {
		glog.Info("Created pcrInfoLong with serialized form %s\n", pcri)
	}

	return pcri, nil
}

// newPCRInfoLong creates and returns a pcrInfoLong structure for the given PCR
// values.
func newPCRInfoLong(f *os.File, loc byte, pcrNums []int) (*pcrInfoLong, error) {
	var mask pcrMask
	for _, pcr := range pcrNums {
		if err := mask.setPCR(pcr); err != nil {
			return nil, err
		}
	}

	if glog.V(2) {
		glog.Infof("mask is % x\n", mask)
	}

	pcrVals, err := FetchPCRValues(f, pcrNums)
	if err != nil {
		return nil, err
	}

	if glog.V(2) {
		glog.Infof("pcrVals is % x\n", pcrVals)
	}

	return createPCRInfoLong(loc, mask, pcrVals)
}