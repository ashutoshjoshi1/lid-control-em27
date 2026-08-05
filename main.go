//go:generate rsrc -manifest main.manifest -o rsrc.syso

package main

import (
	"flag"
	"fmt"
	"sync"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

// slaveAddr is the Modbus slave address of the motor driver.
// Factory default is 1; change here if the drive is configured differently.
const slaveAddr = 1

// LidApp holds all widget references and application state.
type LidApp struct {
	mainWindow *walk.MainWindow

	comPortCB     *walk.ComboBox
	connectBtn    *walk.PushButton
	resetAlarmBtn *walk.PushButton
	openBtn       *walk.PushButton
	closeBtn      *walk.PushButton

	statusLbl    *walk.Label
	alarmLbl     *walk.Label
	positionLbl  *walk.Label
	rainLbl      *walk.Label
	powerFailLbl *walk.Label

	cycleMinEdit  *walk.NumberEdit
	cycleBtn      *walk.PushButton
	cycleStateLbl *walk.Label
	cycleNextLbl  *walk.Label
	cycleTimeLbl  *walk.Label

	driver    MotorDriver
	pollStop  chan struct{}
	connected bool

	openPos  int32
	closePos int32
	posKnown bool

	// Cycle state. Touched only on the UI thread except where noted.
	cycleStop    chan struct{} // closed to stop the cycle goroutine
	cycleReset   chan int      // buffered; carries the op a manual press just ran
	cycleRunning bool
	cycleGen     int    // bumped per start, so a dying loop can't touch its successor
	posState     string // last classified position: "Open"/"Closed"/"Moving"/""

	// cmdMu serialises RunOperation between the UI thread and the cycle
	// goroutine. sendQuery is mutex-guarded per frame, but RunOperation is a
	// multi-frame sequence (READY poll → select+START → reset) that must not
	// interleave with another one.
	cmdMu sync.Mutex
}

func main() {
	logFlag := flag.Bool("log", false, "write debug log to lid_debug.log")
	flag.Parse()
	debugLogging = *logFlag

	app := new(LidApp)
	if err := app.run(); err != nil {
		walk.MsgBox(nil, "Fatal Error", err.Error(), walk.MsgBoxIconError)
	}
}

func (a *LidApp) run() error {
	ports := ListPorts()

	_, err := MainWindow{
		AssignTo: &a.mainWindow,
		Title:    "Lid Controller",
		MinSize:  Size{Width: 300, Height: 320},
		MaxSize:  Size{Width: 300, Height: 0},
		Size:     Size{Width: 300, Height: 320},
		Layout: VBox{
			Margins: Margins{Left: 8, Top: 8, Right: 8, Bottom: 8},
			Spacing: 6,
		},
		Children: []Widget{

			// ── Connection ───────────────────────────────────────────────
			GroupBox{
				Title:  "Connection",
				Layout: Grid{Columns: 2, Spacing: 8},
				Children: []Widget{

					Label{Text: "COM Port:"},
					ComboBox{
						AssignTo:     &a.comPortCB,
						Model:        ports,
						CurrentIndex: 0,
					},

					PushButton{
						AssignTo:  &a.connectBtn,
						Text:      "Connect",
						OnClicked: a.onConnect,
					},
					Label{
						AssignTo: &a.statusLbl,
						Text:     "Disconnected",
					},

					PushButton{
						AssignTo:  &a.resetAlarmBtn,
						Text:      "Reset Alarm",
						Enabled:   false,
						OnClicked: a.onResetAlarm,
					},
					Label{},
				},
			},

			// ── Lid Control ──────────────────────────────────────────────
			GroupBox{
				Title:  "Lid Control",
				Layout: HBox{Spacing: 10},
				Children: []Widget{
					PushButton{
						AssignTo:  &a.openBtn,
						Text:      "▲  Open",
						Enabled:   false,
						OnClicked: a.onOpen,
					},
					PushButton{
						AssignTo:  &a.closeBtn,
						Text:      "▼  Close",
						Enabled:   false,
						OnClicked: a.onClose,
					},
				},
			},

			// ── Cycle ────────────────────────────────────────────────────
			GroupBox{
				Title:  "Cycle",
				Layout: Grid{Columns: 2, Spacing: 8},
				Children: []Widget{

					Label{Text: "Interval (min):"},
					NumberEdit{
						AssignTo:           &a.cycleMinEdit,
						Decimals:           0,
						MinValue:           1,
						MaxValue:           120,
						Value:              defaultCycleMinutes,
						SpinButtonsVisible: true,
						Enabled:            false,
					},

					PushButton{
						AssignTo:  &a.cycleBtn,
						Text:      "Start",
						Enabled:   false,
						OnClicked: a.onCycleToggle,
					},
					Label{
						AssignTo: &a.cycleStateLbl,
						Text:     cycleIdleText,
					},

					Label{
						AssignTo: &a.cycleNextLbl,
						Text:     cycleNextIdleText,
					},
					Label{
						AssignTo: &a.cycleTimeLbl,
						Text:     cycleTimeIdleText,
					},
				},
			},

			// ── Alarm + Position ─────────────────────────────────────────
			Composite{
				Layout: HBox{MarginsZero: true, Spacing: 6},
				Children: []Widget{
					GroupBox{
						Title:         "Alarm",
						Layout:        HBox{},
						MinSize:       Size{Width: 130},
						StretchFactor: 1,
						Children: []Widget{
							Label{AssignTo: &a.alarmLbl, Text: "✔  No Alarm"},
						},
					},
					GroupBox{
						Title:         "Position",
						Layout:        HBox{},
						MinSize:       Size{Width: 130},
						StretchFactor: 1,
						Children: []Widget{
							Label{AssignTo: &a.positionLbl, Text: "---"},
						},
					},
				},
			},

			// ── Inputs ───────────────────────────────────────────────────
			Composite{
				Layout: HBox{MarginsZero: true, Spacing: 6},
				Children: []Widget{
					GroupBox{
						Title:         "Rain",
						Layout:        HBox{},
						MinSize:       Size{Width: 130},
						StretchFactor: 1,
						Children: []Widget{
							Label{AssignTo: &a.rainLbl, Text: "---"},
						},
					},
					GroupBox{
						Title:         "Power Supply",
						Layout:        HBox{},
						MinSize:       Size{Width: 130},
						StretchFactor: 1,
						Children: []Widget{
							Label{AssignTo: &a.powerFailLbl, Text: "---"},
						},
					},
				},
			},
		},
	}.Run()

	return err
}

// ── Button handlers ──────────────────────────────────────────────────────────

func (a *LidApp) onConnect() {
	if a.connected {
		// ── Disconnect ───────────────────────────────────────────────────
		a.stopCycle()
		a.stopAlarmPoller()

		if err := a.driver.Close(); err != nil {
			walk.MsgBox(a.mainWindow, "Disconnect Error", err.Error(), walk.MsgBoxIconWarning)
		}

		a.connected = false
		a.posKnown = false
		a.posState = ""
		a.connectBtn.SetText("Connect")
		a.statusLbl.SetText("Disconnected")
		a.alarmLbl.SetText("✔  No Alarm")
		a.positionLbl.SetText("---")
		a.rainLbl.SetText("---")
		a.powerFailLbl.SetText("---")
		a.openBtn.SetEnabled(false)
		a.closeBtn.SetEnabled(false)
		a.resetAlarmBtn.SetEnabled(false)
		a.cycleBtn.SetEnabled(false)
		a.cycleMinEdit.SetEnabled(false)

	} else {
		// ── Connect ──────────────────────────────────────────────────────
		port := a.comPortCB.Text()
		if err := a.driver.Open(port); err != nil {
			walk.MsgBox(a.mainWindow, "Connection Error",
				fmt.Sprintf("Could not open %s:\n%v", port, err),
				walk.MsgBoxIconError)
			return
		}

		a.connected = true
		a.connectBtn.SetText("Disconnect")
		a.statusLbl.SetText("Connected")
		a.openBtn.SetEnabled(true)
		a.closeBtn.SetEnabled(true)
		a.resetAlarmBtn.SetEnabled(true)
		a.cycleBtn.SetEnabled(true)
		a.cycleMinEdit.SetEnabled(true)

		// Read stored target positions for Open (op 2) and Close (op 3).
		// If this fails the position display will remain "—" but the app works normally.
		if op, err := a.driver.ReadOperationPosition(slaveAddr, OpLidOpen); err == nil {
			if cl, err := a.driver.ReadOperationPosition(slaveAddr, OpLidClose); err == nil {
				a.openPos = op
				a.closePos = cl
				a.posKnown = true
			}
		}

		a.startAlarmPoller()
	}
}

func (a *LidApp) onOpen()  { a.runManual(OpLidOpen, "Open Error") }
func (a *LidApp) onClose() { a.runManual(OpLidClose, "Close Error") }

// runManual issues a lid command off the UI thread so the window stays
// responsive while RunOperation waits for READY (up to ~2 s), and while it
// waits on cmdMu for an in-flight cycle move. If the cycle is running, a
// successful manual move restarts its countdown.
func (a *LidApp) runManual(op int, title string) {
	a.openBtn.SetEnabled(false)
	a.closeBtn.SetEnabled(false)

	go func() {
		a.cmdMu.Lock()
		err := a.driver.RunOperation(slaveAddr, op)
		a.cmdMu.Unlock()

		a.mainWindow.Synchronize(func() {
			a.openBtn.SetEnabled(true)
			a.closeBtn.SetEnabled(true)

			if err != nil {
				walk.MsgBox(a.mainWindow, title, err.Error(), walk.MsgBoxIconError)
				return
			}

			if a.cycleRunning {
				select {
				case a.cycleReset <- op:
				default: // loop already exiting — nothing to reschedule
				}
			}
		})
	}()
}

// onResetAlarm pulses ALM-RST to clear a resettable drive alarm. Runs off the
// UI thread and takes cmdMu like any other command. The alarm poller refreshes
// the Alarm label within 500 ms, so there is nothing to update here.
func (a *LidApp) onResetAlarm() {
	a.resetAlarmBtn.SetEnabled(false)

	go func() {
		a.cmdMu.Lock()
		err := a.driver.ResetAlarm(slaveAddr)
		a.cmdMu.Unlock()

		a.mainWindow.Synchronize(func() {
			a.resetAlarmBtn.SetEnabled(a.connected)
			if err != nil {
				walk.MsgBox(a.mainWindow, "Reset Alarm Error", err.Error(), walk.MsgBoxIconError)
			}
		})
	}()
}

// ── Cycle ────────────────────────────────────────────────────────────────────

const (
	// Must be float64: NumberEdit's Value property asserts float64 and
	// silently falls back to 0 (which is out of range) for any other type.
	defaultCycleMinutes float64 = 5

	cycleTickInterval = 250 * time.Millisecond

	cycleIdleText     = "○  Idle"
	cycleRunningText  = "●  Running"
	cycleNextIdleText = "Next:  ---"
	cycleTimeIdleText = "--:--"
)

// oppositeOp returns the lid operation that reverses op.
func oppositeOp(op int) int {
	if op == OpLidOpen {
		return OpLidClose
	}
	return OpLidOpen
}

// opLabel renders an operation the same way the manual buttons do.
func opLabel(op int) string {
	if op == OpLidOpen {
		return "▲  Open"
	}
	return "▼  Close"
}

// onCycleToggle handles the Start/Stop button.
func (a *LidApp) onCycleToggle() {
	if a.cycleRunning {
		a.stopCycle()
	} else {
		a.startCycle()
	}
}

// startCycle begins alternating the lid on the configured interval, moving
// opposite the current position first. UI thread only.
func (a *LidApp) startCycle() {
	if a.cycleRunning {
		return
	}

	interval := time.Duration(int(a.cycleMinEdit.Value())) * time.Minute

	// First move is whichever direction the lid is not already in.
	first := OpLidOpen
	if a.posState == "Open" {
		first = OpLidClose
	}

	stop := make(chan struct{})
	reset := make(chan int, 1)
	a.cycleStop = stop
	a.cycleReset = reset
	a.cycleRunning = true
	a.cycleGen++

	a.cycleBtn.SetText("Stop")
	a.cycleMinEdit.SetEnabled(false)
	a.cycleStateLbl.SetText(cycleRunningText)

	// The goroutine gets its own channels and generation, so it never races
	// with the UI thread reassigning the fields.
	go a.cycleLoop(a.cycleGen, stop, reset, interval, first)
}

// stopCycle halts the cycle and returns the UI to its idle state.
// Safe to call when no cycle is running. UI thread only.
func (a *LidApp) stopCycle() {
	if a.cycleStop != nil {
		close(a.cycleStop)
		a.cycleStop = nil
	}
	a.resetCycleUI()
}

// cycleFailed tears the cycle down after the loop goroutine has already
// exited on its own, then reports why. UI thread only.
func (a *LidApp) cycleFailed(gen int, title, msg string) {
	if !a.cycleRunning || gen != a.cycleGen {
		return // already stopped, or superseded by a newer cycle
	}
	// The goroutine is gone; drop the channel without closing it so a later
	// stopCycle cannot close it a second time.
	a.cycleStop = nil
	a.resetCycleUI()

	walk.MsgBox(a.mainWindow, title, msg, walk.MsgBoxIconError)
}

// resetCycleUI restores the Cycle widgets to their idle appearance.
func (a *LidApp) resetCycleUI() {
	a.cycleRunning = false
	a.cycleBtn.SetText("Start")
	a.cycleMinEdit.SetEnabled(a.connected)
	a.cycleStateLbl.SetText(cycleIdleText)
	a.cycleNextLbl.SetText(cycleNextIdleText)
	a.cycleTimeLbl.SetText(cycleTimeIdleText)
}

// cycleLoop owns the cycle schedule. It runs on its own goroutine and is
// signalled by the UI thread through cycleStop and cycleReset.
func (a *LidApp) cycleLoop(gen int, stop <-chan struct{}, reset <-chan int, interval time.Duration, nextOp int) {
	ticker := time.NewTicker(cycleTickInterval)
	defer ticker.Stop()

	// The first move fires immediately; the countdown covers the wait to the
	// one after it.
	if !a.cycleMove(gen, nextOp) {
		return
	}
	nextOp = oppositeOp(nextOp)
	deadline := time.Now().Add(interval)
	a.setCycleStatus(gen, nextOp, interval)

	for {
		select {
		case <-stop:
			return

		case op := <-reset:
			// A manual press just moved the lid — restart the countdown and
			// schedule the opposite of whatever was pressed.
			nextOp = oppositeOp(op)
			deadline = time.Now().Add(interval)
			a.setCycleStatus(gen, nextOp, interval)

		case <-ticker.C:
			remain := time.Until(deadline)
			if remain <= 0 {
				if !a.cycleMove(gen, nextOp) {
					return
				}
				nextOp = oppositeOp(nextOp)
				deadline = time.Now().Add(interval)
				remain = interval
			}
			a.setCycleStatus(gen, nextOp, remain)
		}
	}
}

// cycleMove runs one scheduled lid command. It reports false if the move
// failed, in which case the cycle has been torn down and the loop must exit.
func (a *LidApp) cycleMove(gen, op int) bool {
	a.cmdMu.Lock()
	err := a.driver.RunOperation(slaveAddr, op)
	a.cmdMu.Unlock()

	if err != nil {
		a.mainWindow.Synchronize(func() {
			a.cycleFailed(gen, "Cycle Error",
				fmt.Sprintf("Cycle stopped — %s failed:\n%v", opLabel(op), err))
		})
		return false
	}
	return true
}

// setCycleStatus updates the Cycle indicator and countdown from any goroutine.
func (a *LidApp) setCycleStatus(gen, nextOp int, remain time.Duration) {
	if remain < 0 {
		remain = 0
	}
	// Round up so a fresh 5-minute countdown reads 05:00, not 04:59.
	secs := int((remain + time.Second - 1) / time.Second)
	text := fmt.Sprintf("%02d:%02d", secs/60, secs%60)
	next := "Next:  " + opLabel(nextOp)

	a.mainWindow.Synchronize(func() {
		if !a.cycleRunning || gen != a.cycleGen {
			return // stopped or superseded between the send and the callback
		}
		a.cycleStateLbl.SetText(cycleRunningText)
		a.cycleNextLbl.SetText(next)
		a.cycleTimeLbl.SetText(text)
	})
}

// ── Alarm poller ─────────────────────────────────────────────────────────────

// startAlarmPoller launches a background goroutine that polls the alarm
// register every 500 ms and updates the alarm label on the UI thread.
func (a *LidApp) startAlarmPoller() {
	a.pollStop = make(chan struct{})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-a.pollStop:
				return
			case <-ticker.C:
				code, err := a.driver.AlarmCode(slaveAddr)
				if err != nil {
					continue // ignore transient read errors
				}
				status, statusErr := a.driver.StatusWord(slaveAddr)
				pos, posErr := a.driver.Position(slaveAddr)
				din, dinErr := a.driver.DirectInputs(slaveAddr)
				a.driver.LogStatus(slaveAddr)

				a.setAlarm(code != 0, code)
				moving := statusErr == nil && status&statusMOVE != 0
				a.updatePosition(moving, pos, posErr == nil)
				if dinErr == nil {
					a.setInputs(din)
				}
			}
		}
	}()
}

// stopAlarmPoller signals the poller goroutine to exit and waits for it.
func (a *LidApp) stopAlarmPoller() {
	if a.pollStop != nil {
		close(a.pollStop)
		a.pollStop = nil
	}
}

// setAlarm updates the alarm label from any goroutine.
func (a *LidApp) setAlarm(active bool, code int32) {
	a.mainWindow.Synchronize(func() {
		if active {
			a.alarmLbl.SetText(fmt.Sprintf("⚠  0x%04X", code))
			// An alarm aborts the cycle. cycleRunning flips false on the first
			// hit, so a persistent alarm produces exactly one dialog.
			if a.cycleRunning {
				a.stopCycle()
				walk.MsgBox(a.mainWindow, "Cycle Stopped",
					fmt.Sprintf("Cycle stopped — drive alarm 0x%04X.", code),
					walk.MsgBoxIconError)
			}
		} else {
			a.alarmLbl.SetText("✔  No Alarm")
		}
	})
}

// setInputs updates the Rain and Powerfail labels from any goroutine.
func (a *LidApp) setInputs(din uint16) {
	a.mainWindow.Synchronize(func() {
		if din&dinRain != 0 {
			a.rainLbl.SetText("Rain")
		} else {
			a.rainLbl.SetText("Dry")
		}
		if din&dinPowerFail != 0 {
			a.powerFailLbl.SetText("Backup")
		} else {
			a.powerFailLbl.SetText("Normal")
		}
	})
}

// posTolerance is how many encoder steps from a stored target position
// the motor must be within to be considered "Open" or "Closed".
const posTolerance = 500

// updatePosition updates the Position groupbox from any goroutine.
// Priority: Moving > Open > Closed > blank.
func (a *LidApp) updatePosition(moving bool, pos int32, posValid bool) {
	a.mainWindow.Synchronize(func() {
		abs32 := func(v int32) int32 {
			if v < 0 {
				return -v
			}
			return v
		}
		switch {
		case moving:
			a.posState = "Moving"
		case posValid && a.posKnown && abs32(pos-a.openPos) <= posTolerance:
			a.posState = "Open"
		case posValid && a.posKnown && abs32(pos-a.closePos) <= posTolerance:
			a.posState = "Closed"
		default:
			a.posState = ""
		}
		a.positionLbl.SetText(a.posState)
	})
}
