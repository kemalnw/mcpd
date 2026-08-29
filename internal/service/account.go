package service

import (
	"fmt"
	"os"
)

func InstalledAccount() (Account, error) {
	data, err := os.ReadFile(ServiceUnit)
	if err != nil {
		return Account{}, fmt.Errorf("read installed service unit: %w", err)
	}
	return accountByName(unitValue(string(data), "User"))
}

func ChownToInstalledAccount(path string) error {
	account, err := InstalledAccount()
	if err != nil {
		return err
	}
	if err := os.Chown(path, account.UID, account.GID); err != nil {
		return fmt.Errorf("chown %s to %s: %w", path, account.User, err)
	}
	return nil
}
