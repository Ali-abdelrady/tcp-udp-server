package monitor

import (
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"time"
)

// 🔹 Global counter for allocations or API calls
var AllocCounter uint64

// IncrementAlloc safely increases the counter
func IncrementAlloc() {
	atomic.AddUint64(&AllocCounter, 1)
}

// GetAlloc returns the current allocation count
func GetAlloc() uint64 {
	return atomic.LoadUint64(&AllocCounter)
}

// 🔹 Prints runtime stats
func PrintRuntimeStats(label string) {
	m := runtime.MemStats{}
	runtime.ReadMemStats(&m)

	log := fmt.Sprintf(
		"[%v]\n"+
			"Context: %v\n"+
			"GoRoutines: %v\n"+
			"Allocations (custom counter): %v\n"+
			"Memory:\n"+
			"  • Alloc:      %v\n"+
			"  • TotalAlloc: %v\n"+
			"  • Sys:        %v\n"+
			"  • NumGC:      %v\n\n",
		time.Now().UTC().Format("2006-01-02 15:04:05.999999"),
		label,
		runtime.NumGoroutine(),
		GetAlloc(),
		FormatBytesCount(m.Alloc),
		FormatBytesCount(m.TotalAlloc),
		FormatBytesCount(m.Sys),
		m.NumGC,
	)

	appendLogToFile("monitor.log", log)
	fmt.Print(log)
}

// 🔹 Helper for formatting bytes
func FormatBytesCount(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	exp := 0
	for n := b / unit; n >= unit; n /= unit {
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(uint64(1)<<((exp+1)*10)), "KMGTPE"[exp])
}

// 🔹 Writes to file
func appendLogToFile(filename, text string) {
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Println("Failed to open log file:", err)
		return
	}
	defer f.Close()

	f.WriteString(text)
}
