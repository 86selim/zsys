package config

import (
	"os"

	"github.com/k0kubun/pp"
	log "github.com/sirupsen/logrus"
)

// ErrorFormat switch between "%v" and "%+v" depending if we want more verbose info
var ErrorFormat = "%v"

func init() {
	pp.SetDefaultOutput(os.Stderr)
}

// SetVerboseMode change ErrorFormat and logs between verbose and non verbose mode
func SetVerboseMode(verbose bool) {
	if verbose {
		ErrorFormat = "%+v"
		log.SetFormatter(&log.TextFormatter{DisableLevelTruncation: true})
		log.SetLevel(log.DebugLevel)
		log.Debug("verbosity set to debug and will print stacktraces")
	} else {
		ErrorFormat = "%v"
		log.SetFormatter(&log.TextFormatter{
			DisableLevelTruncation: true,
			DisableTimestamp:       true,
		})
		log.SetLevel(log.WarnLevel)
	}
}
