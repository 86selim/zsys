package main

import (
	"io/ioutil"
	"os"
	"os/exec"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"golang.org/x/xerrors"

	"github.com/ubuntu/zsys/internal/config"
	"github.com/ubuntu/zsys/internal/machines"
	"github.com/ubuntu/zsys/internal/zfs"
)

const (
	updateGrubCmd = "update-grub"
)

func main() {
	cmd := generateCommands()

	if err := cmd.Execute(); err != nil {
		log.Error(err)
		os.Exit(1)
	}
}

func generateCommands() *cobra.Command {
	var flagVerbosity int

	var rootCmd = &cobra.Command{
		Use:   "zsys",
		Short: "ZFS SYStem integration control zsys ",
		Long: `Zfs SYStem tool targetting an enhanced ZOL experience.
 It allows running multiple ZFS system in parallels on the same machine,
 get automated snapshots, managing complex zfs dataset layouts separating
 user data from system and persistent data, and more.`,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			config.SetVerboseMode(flagVerbosity)
		},
	}
	rootCmd.PersistentFlags().CountVarP(&flagVerbosity, "verbose", "v", "issue INFO (-v) and DEBUG (-vv) output")

	bootCmd := &cobra.Command{
		Use:       "boot prepare|commit",
		Short:     "Ensure that the right datasets are ready to be mounted and commited during early boot",
		Hidden:    true,
		Args:      cobra.ExactValidArgs(1),
		ValidArgs: []string{"prepare", "commit"},
		Run: func(cmd *cobra.Command, args []string) {
			var err error
			switch args[0] {
			case "prepare":
				err = bootCmd()
			case "commit":
				err = commitCmd()
			}
			if err != nil {
				log.Error(err)
				os.Exit(1)
			}
		},
	}
	rootCmd.AddCommand(bootCmd)

	return rootCmd
}

func bootCmd() (err error) {
	z := zfs.New(zfs.WithTransactions())

	defer func() {
		if err != nil {
			z.Cancel()
			err = xerrors.Errorf("couldn't ensure boot: "+config.ErrorFormat, err)
		} else {
			z.Done()
		}
	}()

	ds, err := z.Scan()
	if err != nil {
		return err
	}
	cmdline, err := procCmdline()
	if err != nil {
		return err
	}
	ms := machines.New(ds, cmdline)

	return ms.EnsureBoot(z)
}

func commitCmd() (err error) {
	z := zfs.New(zfs.WithTransactions())

	defer func() {
		if err != nil {
			z.Cancel()
			err = xerrors.Errorf("couldn't commit: "+config.ErrorFormat, err)
		} else {
			z.Done()
		}
	}()

	ds, err := z.Scan()
	if err != nil {
		return err
	}
	cmdline, err := procCmdline()
	if err != nil {
		return err
	}
	ms := machines.New(ds, cmdline)

	if err := ms.Commit(z); err != nil {
		return err
	}

	// TODO: only run if changes
	cmd := exec.Command(updateGrubCmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return xerrors.Errorf("%q returns an error:"+config.ErrorFormat, updateGrubCmd, err)
	}
	return nil
}

func procCmdline() (string, error) {
	content, err := ioutil.ReadFile("/proc/cmdline")
	if err != nil {
		return "", err
	}

	return string(content), nil
}
