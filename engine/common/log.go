package common

import "fmt"

var LogLevel int = LOG_LEVEL_INFO

const ( // iota is reset to 0
	LOG_LEVEL_VERBOSE  = iota //
	LOG_LEVEL_DEBUG    = iota //
	LOG_LEVEL_INFO     = iota //
	LOG_LEVEL_WARN     = iota //
	LOG_LEVEL_ERROR    = iota //
	LOG_LEVEL_CRITICAL = iota //
)

var LOG_LEVEL_NAMES = map[int]string{
	LOG_LEVEL_VERBOSE:  "VERBOSE",
	LOG_LEVEL_DEBUG:    "DEBUG",
	LOG_LEVEL_INFO:     "INFO",
	LOG_LEVEL_WARN:     "WARN",
	LOG_LEVEL_ERROR:    "ERROR",
	LOG_LEVEL_CRITICAL: "CRITICAL",
}

func doPrintLine(prefix string, format string, v ...any) {
	fmt.Print(prefix)
	fmt.Printf(format, v...)
	fmt.Print("\n")
}

func SetLogLevel(level int) {
	LogLevel = level
	fmt.Printf("  Log level %v\n\n", LOG_LEVEL_NAMES[level])
}

func LogVerbose(format string, v ...any) {
	if LogLevel <= LOG_LEVEL_VERBOSE {
		fmt.Print("[VERBO] ")
		fmt.Printf(format, v...)
	}
}

func LogDebug(format string, v ...any) {
	if LogLevel <= LOG_LEVEL_DEBUG {
		doPrintLine("[DEBUG] ", format, v...)
	}
}

func LogInfo(format string, v ...any) {

	if LogLevel <= LOG_LEVEL_INFO {
		doPrintLine("[INFO]  ", format, v...)
	}
}

func LogWarn(format string, v ...any) {
	if LogLevel <= LOG_LEVEL_WARN {
		doPrintLine("[WARN]  ", format, v...)
	}
}

func LogError(format string, v ...any) {
	if LogLevel <= LOG_LEVEL_ERROR {
		doPrintLine("[ERROR] ", format, v...)
	}
}

func LogCritical(format string, v ...any) {
	if LogLevel <= LOG_LEVEL_CRITICAL {
		doPrintLine("[CRIT]  ", format, v...)
		panic(v)
	}
}
