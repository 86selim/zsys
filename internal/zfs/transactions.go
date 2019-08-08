package zfs

import (
	log "github.com/sirupsen/logrus"

	"github.com/ubuntu/zsys/internal/config"
	"golang.org/x/xerrors"
)

// WithTransactions brings support fake transaction to Zfs.
func WithTransactions() func(z *Zfs) {
	return func(z *Zfs) {
		z.transactional = true
	}
}

// Done ends a transaction and is ready for a new one.
// It will issue a warning if an error occurred during the transaction
func (z *Zfs) Done() {
	if z.transactionErr {
		log.Warnf(config.ErrorFormat+"\n", xerrors.New("An error occurred during a Zfs transaction and Done() was called instead of Cancel()"))
	}
	z.transactionErr = false
	z.reverts = nil
}

// Cancel ends a transaction and try to revert what was possible to revert
func (z *Zfs) Cancel() {
	for i := len(z.reverts) - 1; i >= 0; i-- {
		if err := z.reverts[i](); err != nil {
			log.Warnf(config.ErrorFormat+"\n", xerrors.Errorf("An error occurred when reverting a Zfs transaction: "+config.ErrorFormat, err))
		}
	}
	z.transactionErr = false
	z.reverts = nil
}

// registerRevert is a helper for defer() setting error value
func (z *Zfs) registerRevert(f func() error) {
	z.reverts = append(z.reverts, f)
}

// saveOrRevert stores error in transaction if any or call Cancel in non transactional mode
func (z *Zfs) saveOrRevert(err error) {
	if err == nil {
		// reset for non transactional changes
		if !z.transactional {
			z.reverts = nil
		}
		return
	}
	if z.transactional {
		log.Debugf("ZFS: An error occurred and is saved in the zfs transaction: "+config.ErrorFormat+"\n", err)
		z.transactionErr = true
		return
	}
	z.Cancel()
}
