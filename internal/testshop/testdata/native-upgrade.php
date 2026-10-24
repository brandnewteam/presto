<?php
/*
* 2007-2017 PrestaShop
*
* NOTICE OF LICENSE
*
* This source file is subject to the Open Software License (OSL 3.0)
* that is bundled with this package in the file LICENSE.txt.
* It is also available through the world-wide-web at this URL:
* http://opensource.org/licenses/osl-3.0.php
* If you did not receive a copy of the license and are unable to
* obtain it through the world-wide-web, please send an email
* to license@prestashop.com so we can send you a copy immediately.
*
* DISCLAIMER
*
* Do not edit or add to this file if you wish to upgrade PrestaShop to newer
* versions in the future. If you wish to customize PrestaShop for your
* needs please refer to http://www.prestashop.com for more information.
*
*  @author PrestaShop SA <contact@prestashop.com>
*  @copyright  2007-2017 PrestaShop SA
*  @license    http://opensource.org/licenses/osl-3.0.php  Open Software License (OSL 3.0)
*  International Registered Trademark & Property of PrestaShop SA
*/

// Test fixture: only the upgrade-related methods below are copied without logic changes from
// PrestaShop 1.6.1.22 classes/module/Module.php, retaining its OSL 3.0 notice.
// Upstream: https://github.com/PrestaShop/PrestaShop/blob/1.6.1.22/classes/module/Module.php
// The constructor, loading, and accessors here are offline test scaffolding.
class Module
{
    public $id, $name, $version;
    protected $_errors = array(), $_confirmations = array();
    protected static $modules_cache = array();
    private static $instances = array();
    public function __construct()
    {
        if ($this->name) {
            $this->id = 7;
        }
    }
    public static function getInstanceByName($name)
    {
        if (!isset(self::$instances[$name])) {
            require_once _PS_MODULE_DIR_ . $name . '/' . $name . '.php';
            self::$instances[$name] = new $name();
        }
        return self::$instances[$name];
    }
    public function getErrors()
    {
        return $this->_errors;
    }
    public function getConfirmations()
    {
        return $this->_confirmations;
    }
    protected function setUpgradeMessage($upgrade_detail)
    {
        // Store information if a module has been upgraded (memory optimization)
        if ($upgrade_detail['available_upgrade']) {
            if ($upgrade_detail['success']) {
                $this->_confirmations[] = sprintf(Tools::displayError('Current version: %s'), $this->version);
                $this->_confirmations[] = sprintf(Tools::displayError('%d file upgrade applied'), $upgrade_detail['number_upgraded']);
            } else {
                if (!$upgrade_detail['number_upgraded']) {
                    $this->_errors[] = Tools::displayError('No upgrade has been applied');
                } else {
                    $this->_errors[] = sprintf(Tools::displayError('Upgraded from: %s to %s'), $upgrade_detail['upgraded_from'], $upgrade_detail['upgraded_to']);
                    $this->_errors[] = sprintf(Tools::displayError('%d upgrade left'), $upgrade_detail['number_upgrade_left']);
                }

                if (isset($upgrade_detail['duplicate']) && $upgrade_detail['duplicate']) {
                    $this->_errors[] = sprintf(Tools::displayError('Module %s cannot be upgraded this time: please refresh this page to update it.'), $this->name);
                } else {
                    $this->_errors[] = Tools::displayError('To prevent any problem, this module has been turned off');
                }
            }
        }
    }

    public static function initUpgradeModule($module)
    {
        if (((int)$module->installed == 1) & (empty($module->database_version) === true)) {
            Module::upgradeModuleVersion($module->name, $module->version);
            $module->database_version = $module->version;
        }

        // Init cache upgrade details
        self::$modules_cache[$module->name]['upgrade'] = array(
            'success' => false, // bool to know if upgrade succeed or not
            'available_upgrade' => 0, // Number of available module before any upgrade
            'number_upgraded' => 0, // Number of upgrade done
            'number_upgrade_left' => 0,
            'upgrade_file_left' => array(), // List of the upgrade file left
            'version_fail' => 0, // Version of the upgrade failure
            'upgraded_from' => 0, // Version number before upgrading anything
            'upgraded_to' => 0, // Last upgrade applied
        );

        // Need Upgrade will check and load upgrade file to the moduleCache upgrade case detail
        $ret = $module->installed && Module::needUpgrade($module);
        return $ret;
    }

    public function runUpgradeModule()
    {
        $upgrade = &self::$modules_cache[$this->name]['upgrade'];
        foreach ($upgrade['upgrade_file_left'] as $num => $file_detail) {
            foreach ($file_detail['upgrade_function'] as $item) {
                if (function_exists($item)) {
                    $upgrade['success'] = false;
                    $upgrade['duplicate'] = true;
                    break 2;
                }
            }

            include($file_detail['file']);

            // Call the upgrade function if defined
            $upgrade['success'] = false;
            foreach ($file_detail['upgrade_function'] as $item) {
                if (function_exists($item)) {
                    $upgrade['success'] = $item($this);
                }
            }

            // Set detail when an upgrade succeed or failed
            if ($upgrade['success']) {
                $upgrade['number_upgraded'] += 1;
                $upgrade['upgraded_to'] = $file_detail['version'];

                unset($upgrade['upgrade_file_left'][$num]);
            } else {
                $upgrade['version_fail'] = $file_detail['version'];

                // If any errors, the module is disabled
                $this->disable();
                break;
            }
        }

        $upgrade['number_upgrade_left'] = count($upgrade['upgrade_file_left']);

        // Update module version in DB with the last succeed upgrade
        if ($upgrade['upgraded_to']) {
            Module::upgradeModuleVersion($this->name, $upgrade['upgraded_to']);
        }
        $this->setUpgradeMessage($upgrade);
        return $upgrade;
    }

    public static function upgradeModuleVersion($name, $version)
    {
        return Db::getInstance()->execute('
			UPDATE `' . _DB_PREFIX_ . 'module` m
			SET m.version = \'' . pSQL($version) . '\'
			WHERE m.name = \'' . pSQL($name) . '\'');
    }

    public static function needUpgrade($module)
    {
        self::$modules_cache[$module->name]['upgrade']['upgraded_from'] = $module->database_version;
        // Check the version of the module with the registered one and look if any upgrade file exist
        if (Tools::version_compare($module->version, $module->database_version, '>')) {
            $old_version = $module->database_version;
            $module = Module::getInstanceByName($module->name);
            if ($module instanceof Module) {
                return $module->loadUpgradeVersionList($module->name, $module->version, $old_version);
            }
        }
        return null;
    }

    protected static function loadUpgradeVersionList($module_name, $module_version, $registered_version)
    {
        $list = array();

        $upgrade_path = _PS_MODULE_DIR_ . $module_name . '/upgrade/';

        // Check if folder exist and it could be read
        if (file_exists($upgrade_path) && ($files = scandir($upgrade_path))) {
            // Read each file name
            foreach ($files as $file) {
                if (!in_array($file, array('.', '..', '.svn', 'index.php')) && preg_match('/\.php$/', $file)) {
                    $tab = explode('-', $file);

                    if (!isset($tab[1])) {
                        continue;
                    }

                    $file_version = basename($tab[1], '.php');
                    // Compare version, if minor than actual, we need to upgrade the module
                    if (
                        count($tab) == 2 &&
                        (Tools::version_compare($file_version, $module_version, '<=') &&
                            Tools::version_compare($file_version, $registered_version, '>'))
                    ) {
                        $list[] = array(
                            'file' => $upgrade_path . $file,
                            'version' => $file_version,
                            'upgrade_function' => array(
                                'upgrade_module_' . str_replace('.', '_', $file_version),
                                'upgradeModule' . str_replace('.', '', $file_version)
                            )
                        );
                    }
                }
            }
        }

        // No files upgrade, then upgrade succeed
        if (count($list) == 0) {
            self::$modules_cache[$module_name]['upgrade']['success'] = true;
            Module::upgradeModuleVersion($module_name, $module_version);
        }

        usort($list, 'ps_module_version_sort');

        // Set the list to module cache
        self::$modules_cache[$module_name]['upgrade']['upgrade_file_left'] = $list;
        self::$modules_cache[$module_name]['upgrade']['available_upgrade'] = count($list);
        return (bool)count($list);
    }

    public static function getUpgradeStatus($module_name)
    {
        return (isset(self::$modules_cache[$module_name]) &&
            self::$modules_cache[$module_name]['upgrade']['success']);
    }

    public function disable($force_all = false)
    {
        // Disable module for all shops
        $sql = 'DELETE FROM `' . _DB_PREFIX_ . 'module_shop` WHERE `id_module` = ' . (int)$this->id . ' ' . ((!$force_all) ? ' AND `id_shop` IN(' . implode(', ', Shop::getContextListShopID()) . ')' : '');
        Db::getInstance()->execute($sql);
    }
}

function ps_module_version_sort($a, $b)
{
    return version_compare($a['version'], $b['version']);
}
