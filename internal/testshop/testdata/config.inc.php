<?php
// Offline PrestaShop bootstrap: JSON state replaces SQL, HTTP, cookies, and external services.
define('_PS_VERSION_', isset(prestoState()['ps_version']) ? prestoState()['ps_version'] : '1.6.1.22');
define('_DB_PREFIX_', 'ps_');
define('_PS_MODULE_DIR_', dirname(__DIR__) . '/modules/');
function prestoState()
{
    return json_decode(file_get_contents(__DIR__ . '/state.json'), true);
}
function prestoSave($state)
{
    file_put_contents(__DIR__ . '/state.json', json_encode($state));
}
function prestoEvent($event)
{
    $state = prestoState();
    $state['events'][] = $event;
    prestoSave($state);
}
function pSQL($value)
{
    return addslashes($value);
}
class Context
{
    public $shop, $language, $currency, $country;
    public static function getContext()
    {
        static $context;
        if (!$context) {
            $context = new self();
        }
        return $context;
    }
}
class Shop
{
    const CONTEXT_SHOP = 1;
    const CONTEXT_GROUP = 2;
    const CONTEXT_ALL = 4;
    public $id;
    private static $type = 4;
    private static $selected;
    public function __construct($id)
    {
        $this->id = in_array((int)$id, array_values(self::getShops(false, null, true)), true) ? (int)$id : 0;
    }
    public static function getShops($active = true, $group = null, $ids = false)
    {
        $result = array();
        foreach (prestoState()['shops'] as $shop) {
            if ((!$active || $shop['active']) && (!$group || $group == $shop['group'])) {
                $result[$shop['id']] = $ids ? (int)$shop['id'] : $shop;
            }
        }
        return $result;
    }
    public static function setContext($type, $id = null)
    {
        self::$type = $type;
        self::$selected = $id;
    }
    public static function getContext()
    {
        return self::$type;
    }
    public static function getContextListShopID()
    {
        if (self::$type === 1) {
            return array((int)self::$selected);
        }
        return self::getShops(true, self::$type === 2 ? self::$selected : null, true);
    }
}
class Validate
{
    public static function isLoadedObject($object)
    {
        return $object && $object->id > 0;
    }
}
class Configuration
{
    public static function get($key)
    {
        return 1;
    }
}
class Language
{
    public $id;
    public function __construct($id)
    {
        $this->id = $id;
    }
}
class Currency extends Language {}
class Country extends Language {}
class Tools
{
    public static function version_compare($a, $b, $operator)
    {
        return version_compare($a, $b, $operator);
    }
    public static function displayError($message)
    {
        return $message;
    }
}
class Db
{
    public static function getInstance()
    {
        return new self();
    }
    public function executeS($sql, $array = true, $cache = true)
    {
        if ($cache !== false) {
            throw new Exception('Lifecycle verification must bypass SQL cache');
        }
        $state = prestoState();
        if (strpos($sql, '`ps_module_shop`') !== false) {
            preg_match('/IN \(([^)]+)\)/', $sql, $match);
            $ids = array_map('intval', explode(',', $match[1]));
            return array_map(function ($id) {
                return array('id_shop' => $id);
            }, array_values(array_intersect($state['enabled'], $ids)));
        }
        return $state['installed'] ? array(array('id_module' => 7, 'version' => $state['version'])) : array();
    }
    public function execute($sql)
    {
        $state = prestoState();
        if (strpos($sql, 'DELETE FROM') !== false) {
            if (!empty($state['disable_fails'])) {
                return false;
            }
            preg_match('/IN\(([^)]+)\)/', $sql, $match);
            $ids = array_map('intval', explode(',', $match[1]));
            $state['enabled'] = array_values(array_diff($state['enabled'], $ids));
            $state['disabled_calls'][] = $ids;
        } else {
            preg_match('/SET m.version = \'([^\']+)\'/', $sql, $match);
            if (!$match) {
                throw new Exception('Unexpected fixture SQL: ' . $sql);
            }
            $state['version'] = $match[1];
        }
        prestoSave($state);
        return true;
    }
}
require __DIR__ . '/native-upgrade.php';
