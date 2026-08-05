package main

// motor.go — Modbus RTU driver for the lid motor controller.
// Ported from trackerDriver.cpp / TrackerMotor.cpp.
//
// Protocol:  Modbus RTU over RS-232/RS-485
// Serial:    19200 baud, 8 data bits, Even parity, 1 stop bit
//
// To run a stored operation the manual describes the one-shot NET selection method:
//   Write registers 0x0072–0x0075 in a single FC 0x10 frame:
//     0x0072 = 0x0000          (upper word of NET selection — must be 0; power-up default is 0xFFFF)
//     0x0073 = operation number (lower word; valid range 0–255)
//     0x0074 = 0x0000          (upper word of Driver input 2nd)
//     0x0075 = 0x0008          (lower word; bit 3 = START)
//   Then write 0x0000 to 0x007D to clear START.
//
// IMPORTANT: writing only 0x0073 leaves 0x0072 = 0xFFFF (power-up default).
// The resulting 32-bit value is outside 0–255, so the drive disables NET
// selection and falls back to M0–M7 = all OFF = operation 0.
//
// Lid operations are pre-programmed in the drive:
//   Operation 2 → Open
//   Operation 3 → Close

import (
	"fmt"
	"log"
	"os"
	"sort"
	"sync"
	"time"

	"go.bug.st/serial"
	"golang.org/x/sys/windows/registry"
)

// debugLogging is set by main() based on the -log command-line flag.
var debugLogging bool

// motorLog is the package-level logger that writes to lid_debug.log.
var motorLog *log.Logger

func initLog() {
	if !debugLogging {
		return
	}
	f, err := os.OpenFile("lid_debug.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return
	}
	motorLog = log.New(f, "", log.Ltime|log.Lmicroseconds)
	motorLog.Println("=== lid debug log opened ===")
}

func logf(format string, args ...any) {
	if motorLog != nil {
		motorLog.Printf(format, args...)
	}
}

// ── Stored operation numbers ─────────────────────────────────────────────────

const (
	OpLidOpen  = 2
	OpLidClose = 3
)

// ── Modbus register addresses (from manual + C driver) ───────────────────────

const (
	// One-shot trigger pair with automatic START clear (manual p.292).
	// Writing NET selection + START to this pair triggers the operation and
	// the drive automatically clears START after 250 µs — no second frame needed.
	regNetAutoHi   = 0x0076 // NET selection (auto-OFF pair, high word) — must be 0x0000
	regNetAutoLo   = 0x0077 // NET selection (auto-OFF pair, low word)  — operation number 0–255
	regDriverInLo  = 0x007D // Driver input command  (low word)  — START/STOP bits (reference)
	regStatusLo    = 0x007F // Driver output status  (low word)  — READY/MOVE/INPOS
	regAlarm       = 0x0080 // Alarm code (32-bit, spans 0x0080/0x0081)
	regPosition    = 0x00CC // Feedback position     (32-bit, spans 0x00CC/0x00CD)
	regDirectIOLo  = 0x00D5 // Direct I/O lower word — DIN0..DIN9, VIR-IN (manual p.384)
)

// ── Bit masks for the driver input register (0x007D) ─────────────────────────

const (
	bitStart  = 0x0008 // bit 3 of 0x007D — R-IN3 [START]
	bitAlmRst = 0x0080 // bit 7 of 0x007D — R-IN7 [ALM-RST] (manual p.292)
)

// ── Bit masks for the driver output / status register 0x007F (manual p.293) ──

const (
	statusREADY = 0x0020 // bit  5 — READY: ON when idle, OFF while moving
	statusMOVE  = 0x2000 // bit 13 — MOVE:  ON while motor is running
	statusINPOS = 0x4000 // bit 14 — INPOS: ON when at target position
)

const (
	dinRain      = 0x0001 // DIN0 — Rain sensor input
	dinPowerFail = 0x0004 // DIN2 — Power fail input
)

// ── MotorDriver ──────────────────────────────────────────────────────────────

// MotorDriver manages Modbus RTU communication with a single motor driver unit.
// Slave address 1 is used for all commands (standard factory default).
// Zero value is safe; call Open() before any other method.
type MotorDriver struct {
	mu      sync.Mutex
	port    serial.Port
	readBuf [1024]byte
}

// ── Port enumeration ─────────────────────────────────────────────────────────

// ListPorts returns the COM ports currently registered in the Windows device
// map.  Falls back to COM1–COM20 if the registry key cannot be read.
func ListPorts() []string {
	k, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`HARDWARE\DEVICEMAP\SERIALCOMM`,
		registry.QUERY_VALUE|registry.READ,
	)
	if err != nil {
		return fallbackPorts()
	}
	defer k.Close()

	names, err := k.ReadValueNames(-1)
	if err != nil || len(names) == 0 {
		return fallbackPorts()
	}

	var ports []string
	for _, name := range names {
		val, _, err := k.GetStringValue(name)
		if err == nil {
			ports = append(ports, val)
		}
	}
	if len(ports) == 0 {
		return fallbackPorts()
	}
	sort.Strings(ports)
	return ports
}

func fallbackPorts() []string {
	ports := make([]string, 20)
	for i := range ports {
		ports[i] = fmt.Sprintf("COM%d", i+1)
	}
	return ports
}

// ── Open / Close ─────────────────────────────────────────────────────────────

// Open opens the named serial port with the motor's configured settings:
//
//	19200 baud · 8 data bits · Even parity · 1 stop bit
//	Read timeout 50 ms  (matches C's ReadIntervalTimeout + ReadTotalTimeoutConstant)
func (d *MotorDriver) Open(portName string) error {
	if d.port != nil {
		return nil // already open
	}
	initLog()
	logf("Opening %s  19200 8E1", portName)

	p, err := serial.Open(portName, &serial.Mode{
		BaudRate: 19200,
		DataBits: 8,
		Parity:   serial.EvenParity,
		StopBits: serial.OneStopBit,
	})
	if err != nil {
		logf("ERROR opening port: %v", err)
		return fmt.Errorf("open %s: %w", portName, err)
	}
	if err := p.SetReadTimeout(50 * time.Millisecond); err != nil {
		_ = p.Close()
		logf("ERROR setting timeout: %v", err)
		return fmt.Errorf("set timeout: %w", err)
	}
	d.port = p
	logf("Port open OK")

	// Clear any START that may be latched in the Driver input (2nd) register
	// from a previous session. 0x0075 and 0x007D are independent registers;
	// a prior session that wrote START via 0x0075 without clearing it will
	// hold READY=OFF until we explicitly zero it.
	logf("Clearing driver input registers on connect")
	_ = d.WriteRegister(1, 0x0075, 0x0000) // Driver input (2nd) — clear latched START
	_ = d.WriteRegister(1, 0x007D, 0x0000) // Driver input (reference) — clear too

	return nil
}

// Close closes the serial port.  Safe to call when already closed.
func (d *MotorDriver) Close() error {
	if d.port == nil {
		return nil
	}
	err := d.port.Close()
	d.port = nil
	return err
}

// ── High-level lid commands ──────────────────────────────────────────────────

// RunOperation selects and fires a pre-programmed operation stored in the drive.
//
// Uses the automatic-OFF one-shot pair (manual p.292, registers 0x0076–0x0079):
//   0x0076 = 0x0000   upper word of NET selection (must be 0; power-up default is 0xFFFF)
//   0x0077 = opNum    lower word of NET selection (0–255 selects that operation)
//   0x0078 = 0x0000   upper word of Driver input (automatic OFF)
//   0x0079 = 0x0008   lower word — bit 3 = START, auto-cleared by drive after 250 µs
//
// The drive requires READY = ON before it will accept a START (manual p.69).
// We poll the status register for up to 2 s before giving up.
func (d *MotorDriver) RunOperation(slave, opNum int) error {
	// Poll until READY or 2-second timeout.
	const maxWait = 40
	for i := 0; i < maxWait; i++ {
		status, err := d.readReg16(slave, regStatusLo)
		if err == nil {
			logf("STATUS: 0x%04X  READY=%v  MOVE=%v  INPOS=%v",
				status,
				status&statusREADY != 0,
				status&statusMOVE != 0,
				status&statusINPOS != 0)
			if status&statusREADY != 0 {
				break
			}
		}
		if i == maxWait-1 {
			return fmt.Errorf("drive not READY after %d ms — still moving?", maxWait*50)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// One-shot: NET selection + START in a single frame.
	// Drive auto-clears START after 250 µs — no second write needed.
	buf := []byte{
		0x00, 0x00,        // 0x0076: NET selection upper = 0
		0x00, byte(opNum), // 0x0077: NET selection lower = operation number
		0x00, 0x00,        // 0x0078: Driver input auto-OFF upper = 0
		0x00, 0x08,        // 0x0079: Driver input auto-OFF lower = START (bit 3)
	}
	if err := d.WriteMultipleRegisters(slave, regNetAutoHi, 4, buf); err != nil {
		return fmt.Errorf("select+start operation %d: %w", opNum, err)
	}

	// Reset NET selection to -1 (0xFFFF/0xFFFF) without asserting START.
	// A value outside 0–255 disables NET selection so the digital inputs
	// (DSEL) regain control of operation selection.
	reset := []byte{
		0xFF, 0xFF, // 0x0076: NET selection upper = 0xFFFF
		0xFF, 0xFF, // 0x0077: NET selection lower = 0xFFFF
		0x00, 0x00, // 0x0078: Driver input auto-OFF upper = 0
		0x00, 0x00, // 0x0079: Driver input auto-OFF lower = 0 (no START)
	}
	_ = d.WriteMultipleRegisters(slave, regNetAutoHi, 4, reset)

	return nil
}

// ResetAlarm pulses ALM-RST to clear a resettable drive alarm.
//
// Uses the same automatic-OFF one-shot pair as RunOperation (manual p.292), so
// the drive clears the signal itself after 250 µs and ALM-RST can never stay
// latched. NET selection is held at -1 (0xFFFF) throughout so this cannot
// select or fire a stored operation:
//   0x0076 = 0xFFFF   NET selection upper — no operation selected
//   0x0077 = 0xFFFF   NET selection lower — no operation selected
//   0x0078 = 0x0000   Driver input auto-OFF upper = 0
//   0x0079 = 0x0080   lower word — bit 7 = ALM-RST, auto-cleared by the drive
//
// Note that alarms latched for a reason that is still present (and the
// non-resettable ones) will reassert immediately; the caller should re-read
// the alarm code rather than assume success.
func (d *MotorDriver) ResetAlarm(slave int) error {
	buf := []byte{
		0xFF, 0xFF, // 0x0076: NET selection upper = 0xFFFF (none)
		0xFF, 0xFF, // 0x0077: NET selection lower = 0xFFFF (none)
		0x00, 0x00, // 0x0078: Driver input auto-OFF upper = 0
		0x00, bitAlmRst, // 0x0079: Driver input auto-OFF lower = ALM-RST (bit 7)
	}
	if err := d.WriteMultipleRegisters(slave, regNetAutoHi, 4, buf); err != nil {
		return fmt.Errorf("pulse ALM-RST: %w", err)
	}

	// Leave the auto-OFF pair clear so no input is asserted afterwards.
	clear := []byte{
		0xFF, 0xFF, // 0x0076: NET selection upper = 0xFFFF
		0xFF, 0xFF, // 0x0077: NET selection lower = 0xFFFF
		0x00, 0x00, // 0x0078: Driver input auto-OFF upper = 0
		0x00, 0x00, // 0x0079: Driver input auto-OFF lower = 0
	}
	_ = d.WriteMultipleRegisters(slave, regNetAutoHi, 4, clear)

	return nil
}

// LogStatus reads the status register and writes it to the debug log.
// Called by the alarm poller so READY/MOVE/INPOS appear in every log cycle.
func (d *MotorDriver) LogStatus(slave int) {
	status, err := d.readReg16(slave, regStatusLo)
	if err != nil {
		return
	}
	logf("POLL STATUS: 0x%04X  READY=%v  MOVE=%v  INPOS=%v",
		status,
		status&statusREADY != 0,
		status&statusMOVE != 0,
		status&statusINPOS != 0)
}

// StatusWord reads the raw 16-bit status register value.
func (d *MotorDriver) StatusWord(slave int) (uint16, error) {
	return d.readReg16(slave, regStatusLo)
}

// DirectInputs reads the lower word of the Direct I/O register (0x00D5).
// Bit 0 = DIN0 (Rain), Bit 2 = DIN2 (PowerFail). See manual p.384.
func (d *MotorDriver) DirectInputs(slave int) (uint16, error) {
	return d.readReg16(slave, regDirectIOLo)
}

// ReadOperationPosition reads the 32-bit target position stored in an operation's
// data block using direct reference addresses (manual p.389).
// Base addresses: op 0 → 0x1800, op 1 → 0x1840, ... stride 0x40.
// Position is at offset +2 (32-bit, two consecutive registers).
func (d *MotorDriver) ReadOperationPosition(slave, opNum int) (int32, error) {
	base := 0x1800 + opNum*0x40
	return d.readReg32(slave, base+2)
}

// Stop writes 0 to the driver input register (broadcast slave 0 = all axes).
// Ported from trackerDriver::stopMotors().
func (d *MotorDriver) Stop(slave int) error {
	return d.WriteRegister(slave, regDriverInLo, 0x0000)
}

// ── Status queries ───────────────────────────────────────────────────────────

// IsMoving reads the status register and returns true when the MOVE bit is set.
// Ported from trackerDriver::isMoving().
func (d *MotorDriver) IsMoving(slave int) (bool, error) {
	v, err := d.readReg16(slave, regStatusLo)
	if err != nil {
		return false, err
	}
	return v&statusMOVE != 0, nil
}

// IsInPosition reads the status register and returns true when the INPOS bit is set.
// Ported from trackerDriver::isInPosition().
func (d *MotorDriver) IsInPosition(slave int) (bool, error) {
	v, err := d.readReg16(slave, regStatusLo)
	if err != nil {
		return false, err
	}
	return v&statusINPOS != 0, nil
}

// AlarmCode reads the 32-bit alarm code from the drive.
// Returns 0 when no alarm is active.
// Ported from TrackerMotor::readAllValues() alarm section.
func (d *MotorDriver) AlarmCode(slave int) (int32, error) {
	return d.readReg32(slave, regAlarm)
}

// Position reads the 32-bit feedback position from the drive.
// Ported from TrackerMotor::readCurrentPosition().
func (d *MotorDriver) Position(slave int) (int32, error) {
	return d.readReg32(slave, regPosition)
}

// ── Modbus register read/write primitives ────────────────────────────────────

// WriteRegister writes a 16-bit value to a single Modbus register (FC 0x06).
// Ported from trackerDriver::writeRegister().
func (d *MotorDriver) WriteRegister(slave, reg, value int) error {
	buf := [4]byte{
		byte(reg >> 8), byte(reg),
		byte(value >> 8), byte(value),
	}
	return d.sendQuery(slave, 0x06, buf[:])
}

// WriteMultipleRegisters writes a block of data to consecutive registers (FC 0x10).
// nRegisters is the count of 16-bit registers; data must be at least nRegisters*2 bytes.
// Ported from trackerDriver::writeMultipleRegisters().
func (d *MotorDriver) WriteMultipleRegisters(slave, regBase, nRegisters int, data []byte) error {
	dataLen := nRegisters * 2
	buf := make([]byte, 5+dataLen)
	buf[0] = byte(regBase >> 8)
	buf[1] = byte(regBase)
	buf[2] = byte(nRegisters >> 8)
	buf[3] = byte(nRegisters)
	buf[4] = byte(dataLen)
	copy(buf[5:], data[:dataLen])
	return d.sendQuery(slave, 0x10, buf)
}

// InsertIntoBuffer packs a big-endian int32 into buf at position and returns 4.
// Ported from trackerDriver::insertIntoBuffer().
func InsertIntoBuffer(v int32, buf []byte, pos int) int {
	buf[pos+0] = byte(v >> 24)
	buf[pos+1] = byte(v >> 16)
	buf[pos+2] = byte(v >> 8)
	buf[pos+3] = byte(v)
	return 4
}

// ── Internal helpers ─────────────────────────────────────────────────────────

// readReg16 reads one 16-bit Modbus register and returns its value.
func (d *MotorDriver) readReg16(slave, reg int) (uint16, error) {
	buf := [4]byte{byte(reg >> 8), byte(reg), 0x00, 0x01}
	if err := d.sendQuery(slave, 0x03, buf[:]); err != nil {
		return 0, err
	}
	// FC 0x03 response layout: [slave][0x03][byteCount][hi][lo]...
	return uint16(d.readBuf[3])<<8 | uint16(d.readBuf[4]), nil
}

// readReg32 reads two consecutive 16-bit registers and returns them as a
// big-endian int32 — matching the C driver's readFromBuffer(readBuffer, 3).
func (d *MotorDriver) readReg32(slave, reg int) (int32, error) {
	buf := [4]byte{byte(reg >> 8), byte(reg), 0x00, 0x02}
	if err := d.sendQuery(slave, 0x03, buf[:]); err != nil {
		return 0, err
	}
	v := uint32(d.readBuf[3])<<24 |
		uint32(d.readBuf[4])<<16 |
		uint32(d.readBuf[5])<<8 |
		uint32(d.readBuf[6])
	return int32(v), nil
}

// sendQuery builds a Modbus RTU frame, transmits it, and (for non-broadcast
// slaves) reads the response into d.readBuf.
// Ported from trackerDriver::sendQuery() + trackerDriver::getResponse().
func (d *MotorDriver) sendQuery(slave, function int, data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.port == nil {
		return fmt.Errorf("port not open")
	}

	// Flush any stale bytes left in the OS receive buffer (e.g. from a
	// concurrent alarm poll response arriving just before this command).
	if err := d.port.ResetInputBuffer(); err != nil {
		logf("FLUSH ERROR: %v", err)
	}

	// Build frame: [slave][function][...data][CRC_lo][CRC_hi]
	frameLen := 2 + len(data)
	frame := make([]byte, frameLen+2)
	frame[0] = byte(slave)
	frame[1] = byte(function)
	copy(frame[2:], data)
	crc := calculateCRC16(frame[:frameLen])
	frame[frameLen] = byte(crc)
	frame[frameLen+1] = byte(crc >> 8)

	logf("TX slave=%d fc=0x%02X  %s", slave, function, hexDump(frame))

	if _, err := d.port.Write(frame); err != nil {
		logf("TX ERROR: %v", err)
		return fmt.Errorf("serial write: %w", err)
	}

	if slave > 0 {
		// Calculate the number of bytes the drive will echo back.
		// FC 0x03 (read registers): [slave][fc][byteCount][data 2*nRegs bytes][crcLo][crcHi]
		// FC 0x06 and FC 0x10 (writes): always an 8-byte echo of the request header.
		var expectedLen int
		if function == 0x03 {
			nRegs := int(data[2])<<8 | int(data[3])
			expectedLen = 5 + 2*nRegs
		} else {
			expectedLen = 8
		}

		// Loop-read until we have all expected bytes or a 200 ms overall deadline.
		// A single Read() may return fewer bytes than available if the OS hasn't
		// yet received the full frame, so we accumulate across multiple calls.
		total := 0
		deadline := time.Now().Add(200 * time.Millisecond)
		for total < expectedLen && time.Now().Before(deadline) {
			n, err := d.port.Read(d.readBuf[total:])
			total += n
			if err != nil {
				logf("RX ERROR after %d bytes: %v", total, err)
				break
			}
			if n == 0 {
				break // read timeout — no more bytes arriving
			}
		}

		if total == 0 {
			logf("RX ERROR: no response from slave %d (fc=0x%02X)", slave, function)
			return fmt.Errorf("no response from slave %d (fc=0x%02X)", slave, function)
		}
		logf("RX %d bytes  %s", total, hexDump(d.readBuf[:total]))
		time.Sleep(1 * time.Millisecond)
	} else {
		// Broadcast (slave 0) — no response expected; wait for bus to settle.
		logf("RX (broadcast — no response expected)")
		time.Sleep(5 * time.Millisecond) // matches C driver's >10000-baud delay
	}
	return nil
}

// hexDump formats a byte slice as space-separated hex: "01 06 00 73 00 02 ..."
func hexDump(b []byte) string {
	s := ""
	for i, v := range b {
		if i > 0 {
			s += " "
		}
		s += fmt.Sprintf("%02X", v)
	}
	return s
}

// calculateCRC16 computes the Modbus CRC-16 checksum.
// Ported verbatim from trackerDriver::calculateCRC16() (MODBUSTOOLS.COM table).
func calculateCRC16(buf []byte) uint16 {
	crcTable := [256]uint16{
		0x0000, 0xC0C1, 0xC181, 0x0140, 0xC301, 0x03C0, 0x0280, 0xC241,
		0xC601, 0x06C0, 0x0780, 0xC741, 0x0500, 0xC5C1, 0xC481, 0x0440,
		0xCC01, 0x0CC0, 0x0D80, 0xCD41, 0x0F00, 0xCFC1, 0xCE81, 0x0E40,
		0x0A00, 0xCAC1, 0xCB81, 0x0B40, 0xC901, 0x09C0, 0x0880, 0xC841,
		0xD801, 0x18C0, 0x1980, 0xD941, 0x1B00, 0xDBC1, 0xDA81, 0x1A40,
		0x1E00, 0xDEC1, 0xDF81, 0x1F40, 0xDD01, 0x1DC0, 0x1C80, 0xDC41,
		0x1400, 0xD4C1, 0xD581, 0x1540, 0xD701, 0x17C0, 0x1680, 0xD641,
		0xD201, 0x12C0, 0x1380, 0xD341, 0x1100, 0xD1C1, 0xD081, 0x1040,
		0xF001, 0x30C0, 0x3180, 0xF141, 0x3300, 0xF3C1, 0xF281, 0x3240,
		0x3600, 0xF6C1, 0xF781, 0x3740, 0xF501, 0x35C0, 0x3480, 0xF441,
		0x3C00, 0xFCC1, 0xFD81, 0x3D40, 0xFF01, 0x3FC0, 0x3E80, 0xFE41,
		0xFA01, 0x3AC0, 0x3B80, 0xFB41, 0x3900, 0xF9C1, 0xF881, 0x3840,
		0x2800, 0xE8C1, 0xE981, 0x2940, 0xEB01, 0x2BC0, 0x2A80, 0xEA41,
		0xEE01, 0x2EC0, 0x2F80, 0xEF41, 0x2D00, 0xEDC1, 0xEC81, 0x2C40,
		0xE401, 0x24C0, 0x2580, 0xE541, 0x2700, 0xE7C1, 0xE681, 0x2640,
		0x2200, 0xE2C1, 0xE381, 0x2340, 0xE101, 0x21C0, 0x2080, 0xE041,
		0xA001, 0x60C0, 0x6180, 0xA141, 0x6300, 0xA3C1, 0xA281, 0x6240,
		0x6600, 0xA6C1, 0xA781, 0x6740, 0xA501, 0x65C0, 0x6480, 0xA441,
		0x6C00, 0xACC1, 0xAD81, 0x6D40, 0xAF01, 0x6FC0, 0x6E80, 0xAE41,
		0xAA01, 0x6AC0, 0x6B80, 0xAB41, 0x6900, 0xA9C1, 0xA881, 0x6840,
		0x7800, 0xB8C1, 0xB981, 0x7940, 0xBB01, 0x7BC0, 0x7A80, 0xBA41,
		0xBE01, 0x7EC0, 0x7F80, 0xBF41, 0x7D00, 0xBDC1, 0xBC81, 0x7C40,
		0xB401, 0x74C0, 0x7580, 0xB541, 0x7700, 0xB7C1, 0xB681, 0x7640,
		0x7200, 0xB2C1, 0xB381, 0x7340, 0xB101, 0x71C0, 0x7080, 0xB041,
		0x5000, 0x90C1, 0x9181, 0x5140, 0x9301, 0x53C0, 0x5280, 0x9241,
		0x9601, 0x56C0, 0x5780, 0x9741, 0x5500, 0x95C1, 0x9481, 0x5440,
		0x9C01, 0x5CC0, 0x5D80, 0x9D41, 0x5F00, 0x9FC1, 0x9E81, 0x5E40,
		0x5A00, 0x9AC1, 0x9B81, 0x5B40, 0x9901, 0x59C0, 0x5880, 0x9841,
		0x8801, 0x48C0, 0x4980, 0x8941, 0x4B00, 0x8BC1, 0x8A81, 0x4A40,
		0x4E00, 0x8EC1, 0x8F81, 0x4F40, 0x8D01, 0x4DC0, 0x4C80, 0x8C41,
		0x4400, 0x84C1, 0x8581, 0x4540, 0x8701, 0x47C0, 0x4680, 0x8641,
		0x8201, 0x42C0, 0x4380, 0x8341, 0x4100, 0x81C1, 0x8081, 0x4040,
	}

	crc := uint16(0xFFFF)
	for _, b := range buf {
		crc = (crc >> 8) ^ crcTable[byte(crc)^b]
	}
	return crc
}
