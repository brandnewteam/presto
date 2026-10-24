package config

import (
	"fmt"
	"strings"
)

// PrestaShop enables the native 1.6 lifecycle for a single target.
// No context is inferred: migrations share a global module version in PrestaShop.
type PrestaShop struct {
	Upgrade     bool     `toml:"upgrade" json:"upgrade"`
	PHP         []string `toml:"php" json:"php"`
	ShopContext string   `toml:"shop_context" json:"shop_context"`
	ShopID      int      `toml:"shop_id" json:"shop_id,omitempty"`
	ShopGroupID int      `toml:"shop_group_id" json:"shop_group_id,omitempty"`
}

func (p PrestaShop) Validate() error {
	if p.PHP != nil && len(p.PHP) == 0 {
		return fmt.Errorf("prestashop.php cannot be an empty command array")
	}
	if len(p.PHP) > 0 && strings.TrimSpace(p.PHP[0]) == "" {
		return fmt.Errorf("prestashop.php must start with an executable")
	}
	for _, arg := range p.PHP {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("invalid NUL in prestashop.php")
		}
	}
	if !p.Upgrade {
		return nil
	}
	switch p.ShopContext {
	case "all":
		if p.ShopID != 0 || p.ShopGroupID != 0 {
			return fmt.Errorf("shop_context = all cannot have shop_id or shop_group_id")
		}
	case "shop":
		if p.ShopID <= 0 || p.ShopGroupID != 0 {
			return fmt.Errorf("shop_context = shop requires a positive shop_id and no shop_group_id")
		}
	case "group":
		if p.ShopGroupID <= 0 || p.ShopID != 0 {
			return fmt.Errorf("shop_context = group requires a positive shop_group_id and no shop_id")
		}
	default:
		return fmt.Errorf("prestashop.upgrade requires explicit shop_context = all, shop, or group")
	}
	return nil
}

func (p PrestaShop) Command() []string {
	if len(p.PHP) == 0 {
		return []string{"php"}
	}
	return append([]string{}, p.PHP...)
}
