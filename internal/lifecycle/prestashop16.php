<?php

/**
 * Private CLI adapter for Presto. It is embedded in the Go executable and never installed in a shop.
 * Keep syntax compatible with PHP 7.1 and delegate migrations to PrestaShop 1.6 itself.
 */

if (PHP_SAPI !== 'cli' || !isset($argv[1])) {
    exit(1);
}
$prestoRequest = json_decode(file_get_contents($argv[1]), true);
if (!is_array($prestoRequest) || !isset($prestoRequest['result_path'])) {
    exit(1);
}
$prestoResult = array(
    'status' => 'failed',
    'shop_context' => $prestoRequest['settings']['shop_context'],
    'shop_id' => isset($prestoRequest['settings']['shop_id']) ? $prestoRequest['settings']['shop_id'] : 0,
    'shop_group_id' => isset($prestoRequest['settings']['shop_group_id']) ? $prestoRequest['settings']['shop_group_id'] : 0,
    'shop_ids' => array(),
    'installed' => false,
    'enabled_shop_ids' => null,
    'applied' => 0,
    'disabled' => false,
    'messages' => array()
);
$prestoFinished = false;
register_shutdown_function(function () use (&$prestoFinished, &$prestoResult, $prestoRequest) {
    if (!$prestoFinished) {
        $error = error_get_last();
        $prestoResult['error'] = $error ? $error['message'] : 'PHP exited before completing the lifecycle operation';
        file_put_contents($prestoRequest['result_path'], json_encode($prestoResult));
    }
});

function prestoModuleRow($name)
{
    $rows = Db::getInstance()->executeS('SELECT id_module, version FROM `' . _DB_PREFIX_ . 'module` WHERE name = \'' . pSQL($name) . '\'', true, false);
    if (!is_array($rows)) {
        throw new Exception('Cannot read the installed module record');
    }
    return count($rows) ? $rows[0] : null;
}

function prestoEnabledShops($moduleId, $ids)
{
    if (!count($ids)) {
        return array();
    }
    $rows = Db::getInstance()->executeS('SELECT id_shop FROM `' . _DB_PREFIX_ . 'module_shop` WHERE id_module = ' . (int)$moduleId . ' AND id_shop IN (' . implode(',', array_map('intval', $ids)) . ')', true, false);
    if (!is_array($rows)) {
        throw new Exception('Cannot verify module activation state');
    }
    return array_map('intval', array_column($rows, 'id_shop'));
}

function prestoContext($settings)
{
    $scope = $settings['shop_context'];
    $all = array_map('intval', array_values(Shop::getShops(false, null, true)));
    if ($scope === 'shop') {
        $id = (int)$settings['shop_id'];
        if (!in_array($id, $all, true)) {
            throw new Exception('Configured shop does not exist');
        }
        $ids = array($id);
        $type = Shop::CONTEXT_SHOP;
    } elseif ($scope === 'group') {
        $id = (int)$settings['shop_group_id'];
        $ids = array_map('intval', array_values(Shop::getShops(false, $id, true)));
        $type = Shop::CONTEXT_GROUP;
    } elseif ($scope === 'all') {
        $id = null;
        $ids = $all;
        $type = Shop::CONTEXT_ALL;
    } else {
        throw new Exception('An explicit shop context is required');
    }
    if (!count($ids)) {
        throw new Exception('Configured context contains no shops');
    }
    sort($ids, SORT_NUMERIC);
    $defaultShop = (int)Configuration::get('PS_SHOP_DEFAULT');
    $anchor = in_array($defaultShop, $ids, true) ? $defaultShop : $ids[0];
    Context::getContext()->shop = new Shop($anchor);
    if (!Validate::isLoadedObject(Context::getContext()->shop)) {
        throw new Exception('Cannot initialize the selected shop');
    }
    Shop::setContext($type, $id);
    $context = Context::getContext();
    $context->language = new Language((int)Configuration::get('PS_LANG_DEFAULT'));
    $context->currency = new Currency((int)Configuration::get('PS_CURRENCY_DEFAULT'));
    $context->country = new Country((int)Configuration::get('PS_COUNTRY_DEFAULT'), (int)$context->language->id);
    return $ids;
}

function prestoRun($request, &$result)
{
    $name = $request['module'];
    if (!is_string($name) || !preg_match('/^[a-z][a-z0-9_]*$/D', $name)) {
        throw new Exception('Invalid module name');
    }
    if (!defined('_PS_VERSION_') || !preg_match('/^1\.6\./', _PS_VERSION_)) {
        throw new Exception('This lifecycle adapter supports PrestaShop 1.6 only');
    }
    $ids = prestoContext($request['settings']);
    $result['shop_ids'] = $ids;
    $row = prestoModuleRow($name);
    if (!$row) {
        if ($request['operation'] === 'disable' && isset($request['before']['module_id'])) {
            // An upgrade may have uninstalled itself: confirm no activation rows remain.
            if (count(prestoEnabledShops($request['before']['module_id'], $request['before']['shop_ids']))) {
                throw new Exception('Module record disappeared but activation rows remain');
            }
            $result['enabled_shop_ids'] = array();
            $result['disabled'] = true;
            $result['shop_ids'] = $request['before']['shop_ids'];
            $result['status'] = 'disabled';
        } elseif ($request['operation'] === 'upgrade' && !empty($request['before']['installed'])) {
            throw new Exception('Installed module disappeared during synchronization');
        } else {
            $result['status'] = 'not_installed';
        }
        return;
    }
    $result['installed'] = true;
    $result['module_id'] = (int)$row['id_module'];
    $result['from_version'] = $row['version'];
    $result['to_version'] = $row['version'];
    $result['enabled_shop_ids'] = prestoEnabledShops($row['id_module'], $ids);
    if ($request['operation'] === 'inspect') {
        if ($request['new_version'] !== '' && version_compare($request['new_version'], $row['version'], '<')) {
            throw new Exception('Refusing to downgrade an installed module');
        }
        $result['status'] = 'inspected';
        return;
    }
    $before = $request['before'];
    if (!$before || empty($before['installed']) || (int)$before['module_id'] !== (int)$row['id_module']) {
        throw new Exception('Installed module identity changed during synchronization');
    }
    if ($request['operation'] === 'disable') {
        $result['enabled_shop_ids'] = null;
        // Avoid instantiating the deployed class, which may throw or have a parse error.
        // Per-shop calls also cover inactive shops omitted by 1.6's ALL/GROUP disable().
        foreach ($before['shop_ids'] as $shopId) {
            if (!in_array((int)$shopId, array_map('intval', array_values(Shop::getShops(false, null, true))), true)) {
                throw new Exception('A selected shop disappeared before deactivation');
            }
            Context::getContext()->shop = new Shop((int)$shopId);
            Shop::setContext(Shop::CONTEXT_SHOP, (int)$shopId);
            $base = new Module();
            $base->id = (int)$row['id_module'];
            $base->disable();
        }
        $result['enabled_shop_ids'] = prestoEnabledShops($row['id_module'], $before['shop_ids']);
        if (count($result['enabled_shop_ids'])) {
            throw new Exception('PrestaShop did not disable the module in every selected shop');
        }
        $result['shop_ids'] = $before['shop_ids'];
        $result['disabled'] = true;
        $result['status'] = 'disabled';
        return;
    }
    if ($request['operation'] !== 'upgrade') {
        throw new Exception('Unknown lifecycle operation');
    }
    if ($row['version'] !== $before['from_version'] || $ids !== $before['shop_ids']) {
        throw new Exception('Module version or shop scope changed during synchronization');
    }
    $result['enabled_shop_ids'] = null;
    $module = Module::getInstanceByName($name);
    if (!$module || !($module instanceof Module)) {
        throw new Exception('Cannot instantiate the deployed module');
    }
    if (version_compare($module->version, $row['version'], '<')) {
        throw new Exception('Refusing to downgrade an installed module');
    }
    if ($request['new_version'] !== '' && $module->version !== $request['new_version']) {
        throw new Exception('Loaded module version disagrees with the prepared package');
    }
    if (!version_compare($module->version, $row['version'], '>')) {
        $result['enabled_shop_ids'] = prestoEnabledShops($row['id_module'], $ids);
        $result['status'] = 'up_to_date';
        return;
    }
    $descriptor = new stdClass();
    $descriptor->name = $name;
    $descriptor->installed = true;
    $descriptor->version = $module->version;
    $descriptor->database_version = $row['version'];
    if (Module::initUpgradeModule($descriptor)) {
        $details = $module->runUpgradeModule();
        $result['applied'] = isset($details['number_upgraded']) ? (int)$details['number_upgraded'] : 0;
        $result['messages'] = array_values(array_merge($module->getConfirmations(), $module->getErrors()));
        if (empty($details['success']) || !empty($details['number_upgrade_left']) || count($module->getErrors())) {
            $failed = isset($details['version_fail']) ? $details['version_fail'] : 'unknown';
            throw new Exception('Native module upgrade failed at version ' . $failed);
        }
        // 1.6 may register only the last script version. Re-enter its native no-script
        // path to register the package version without rerunning successful scripts.
        $after = prestoModuleRow($name);
        if (!$after) {
            throw new Exception('Module disappeared during upgrade');
        }
        $descriptor->database_version = $after['version'];
        if (version_compare($module->version, $after['version'], '>') && Module::initUpgradeModule($descriptor)) {
            throw new Exception('Upgrade scripts remain pending after finalization');
        }
    }
    $after = prestoModuleRow($name);
    if (!$after || $after['version'] !== $module->version) {
        throw new Exception('Registered module version did not reach the deployed version');
    }
    $result['to_version'] = $after['version'];
    $result['enabled_shop_ids'] = prestoEnabledShops($after['id_module'], $ids);
    $result['status'] = 'upgraded';
}

try {
    // Avoid passing adapter arguments through PrestaShop's argvToGET conversion.
    $argc = 1;
    $argv = array(__FILE__);
    $_SERVER['REQUEST_URI'] = '/';
    $_SERVER['SCRIPT_NAME'] = '/index.php';
    $_SERVER['HTTP_HOST'] = 'localhost';
    $_SERVER['REQUEST_METHOD'] = 'GET';
    if ($prestoRequest['settings']['shop_context'] === 'shop') {
        $_GET['id_shop'] = (int)$prestoRequest['settings']['shop_id'];
    }
    require $prestoRequest['root'] . '/config/config.inc.php';
    prestoRun($prestoRequest, $prestoResult);
} catch (Throwable $error) {
    $prestoResult['status'] = 'failed';
    $prestoResult['error'] = $error->getMessage();
}
if (file_put_contents($prestoRequest['result_path'], json_encode($prestoResult)) === false) {
    exit(1);
}
$prestoFinished = true;
exit(isset($prestoResult['error']) ? 1 : 0);
