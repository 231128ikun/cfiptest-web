// app.js ↔ index.html 接线门禁。
//
// 交接文档一直把 app.js 记为「零覆盖，只能靠浏览器走查」。走查确实无可替代，
// 但其中最致命、又最容易被眼睛漏掉的一类问题是纯机械的：app.js 摸了一个
// index.html 里不存在的 id → getElementById 返回 null → addEventListener 抛错 →
// 该模块后面的接线全部静默失效。这个脚本把那一类钉死，不需要浏览器。
//
// 覆盖不到的（仍需人眼）：视觉、真实事件流、SSE、虚拟滚动手感。
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const html = readFileSync(new URL('../web/index.html', import.meta.url), 'utf8');
const appJs = readFileSync(new URL('../web/js/app.js', import.meta.url), 'utf8');
const tasksJs = readFileSync(new URL('../web/js/tasks.js', import.meta.url), 'utf8');
const quotaRulesJs = readFileSync(new URL('../web/js/quota-rules.js', import.meta.url), 'utf8');
const css = readFileSync(new URL('../web/css/style.css', import.meta.url), 'utf8');

let pass = 0;
const check = (name, fn) => {
    try { fn(); pass++; console.log(`  ok  ${name}`); }
    catch (e) { console.log(`  FAIL ${name}\n       ${e.message}`); process.exitCode = 1; }
};

// index.html 里声明的 id（含重复，用于查重）
const declaredIds = [...html.matchAll(/\bid="([^"]+)"/g)].map(m => m[1]);
const idSet = new Set(declaredIds);

// app.js 里 $('x') 形式的引用。$ = id => document.getElementById(id)，见 app.js:17，
// 所以这就是 app.js 全部的 id 取用入口。
const referencedIds = [...new Set([...appJs.matchAll(/\$\('([^']+)'\)/g)].map(m => m[1]))].sort();

console.log('id 接线:');
check('app.js 引用的每个 id 都在 index.html 里存在', () => {
    const missing = referencedIds.filter(id => !idSet.has(id));
    assert.deepEqual(missing, [], `这些 id 会取到 null：${missing.join(', ')}`);
});
check('tasks.js 引用的每个 id 都在 index.html 里存在', () => {
    const missing = [...new Set([...tasksJs.matchAll(/\$\('([^']+)'\)/g)].map(m => m[1]))].filter(id => !idSet.has(id));
    assert.deepEqual(missing, [], `这些 id 会取到 null：${missing.join(', ')}`);
});
check('检测参数统一在设置页控制，任务级覆盖已移除', () => {
    assert.ok(/id="auto-lat-concurrency"/.test(html) && /id="auto-spd-concurrency"/.test(html), '设置页缺少自动维护并发输入');
    assert.ok(/id="auto-lat-timeout"/.test(html) && /id="auto-lat-probes"/.test(html) && /id="auto-lat-http-probes"/.test(html), '设置页缺少超时/探测次数输入');
    assert.ok(/autoLatencyConcurrency: positiveInt\('auto-lat-concurrency'\)/.test(appJs), '设置页并发未写入保存对象');
    assert.ok(/\$\(\'auto-lat-concurrency\'\)\.value = settings\.autoLatencyConcurrency/.test(appJs), '设置页并发未回填');
    assert.equal(/id="task-lat-concurrency"/.test(html), false, '任务弹窗不应再有任务级并发输入');
    assert.equal(/id="task-lat-timeout"/.test(html), false, '任务弹窗不应再有任务级超时输入');
    assert.equal(/taskConcurrency\(/.test(tasksJs), false, '任务表单不应再收集任务级检测参数');
    assert.equal(/task-max-candidates|maxCandidatesRaw|task\.maxCandidates = /.test(tasksJs), false, '任务表单不应再收集单次检测上限');
});
check('引用面不是空的（正则失效时这条会先炸）', () => {
    assert.ok(referencedIds.length > 60, `只解析出 ${referencedIds.length} 个引用，正则可能已失配`);
});
check('TXT 导出模板为稳定单列布局，CSV 时完整隐藏', () => {
    assert.ok(/id="export-template-field"/.test(html), '缺少可整体隐藏的导出模板区域');
    assert.ok(/id="btn-template-toggle"[^>]*aria-label="添加 TXT 模板"[^>]*>＋<\/button>/.test(html), '模板入口不是独立的小加号按钮');
    assert.ok(/templateField\.hidden = true/.test(appJs), 'CSV 格式没有隐藏导出模板区域');
    assert.ok(/templateField\.hidden = false/.test(appJs), 'TXT 格式没有恢复导出模板区域');
});
check('维护任务开关位于卡片状态区并即时重绘状态', () => {
    assert.ok(/class="task-card-state"/.test(tasksJs), '任务卡片缺少右侧状态区域');
    assert.ok(/addEventListener\('change'[\s\S]*?updateTaskEnabled/.test(tasksJs), '任务启动开关没有 change 事件接线');
    assert.ok(/task\.enabled = enabled;[\s\S]*?renderTaskGrid\(\);[\s\S]*?await api\.saveTasks/.test(tasksJs), '任务状态没有先即时更新再持久化');
});
check('导入先弹窗选择目标库，不再直接在导出面板选择', () => {
    assert.ok(/id="import-target-modal"/.test(html), '缺少导入目标弹窗');
    assert.ok(/id="btn-import-target-confirm"/.test(html), '缺少确认导入按钮');
    const exportBlock = html.slice(html.indexOf('export-actions-right'), html.indexOf('export-actions-right') + 600);
    assert.equal(exportBlock.includes('lib-target-select'), false, '导出面板不应再内嵌目标库下拉');
    const modalBlock = html.slice(html.indexOf('import-target-modal'), html.indexOf('import-target-modal') + 1500);
    assert.ok(/id="lib-target-select"/.test(modalBlock), '目标库下拉应位于导入弹窗内');
    assert.ok(/openImportTargetModal/.test(appJs), 'app.js 缺少打开弹窗函数');
    assert.ok(/closeImportTargetModal/.test(appJs), 'app.js 缺少关闭弹窗函数');
    assert.ok(/btn-import-target-confirm/.test(appJs), '确认导入未接线');
});
check('自动维护支持 Cron 定时，并实时解释表达式含义', () => {
    assert.ok(/id="task-schedule-cron"/.test(html), '缺少 Cron 表达式输入框');
    assert.ok(/id="task-schedule-description"/.test(html), '缺少 Cron 释义小字');
    assert.ok(/id="task-schedule-enabled"/.test(html), '缺少定时开关');
    assert.ok(/function describeCron/.test(tasksJs), 'tasks.js 缺少 Cron 解析');
    assert.ok(/function updateScheduleUI/.test(tasksJs), 'tasks.js 缺少释义刷新逻辑');
    assert.ok(/task-schedule-cron/.test(tasksJs), 'Cron 输入未接入表单');
    assert.ok(/task\.schedule = \{/.test(tasksJs), '保存时未收集定时配置');
});
check('任务卡片展示定时摘要', () => {
    assert.ok(/scheduleLabel\(task\.schedule\)/.test(tasksJs), '卡片缺少定时摘要');
    assert.ok(/is-scheduled/.test(tasksJs), '卡片缺少定时状态样式标记');
});
check('index.html 没有重复 id（重复的话 getElementById 只取第一个）', () => {
    const dup = declaredIds.filter((id, i) => declaredIds.indexOf(id) !== i);
    assert.deepEqual([...new Set(dup)], []);
});
check('版本号按“版本号：时间”格式显示', () => {
    assert.ok(/id="app-version">版本号：加载中/.test(html), '页面缺少版本号占位');
    assert.ok(/\$\('app-version'\)\.textContent = `版本号：\$\{config\.version \|\| 'dev'\}`/.test(appJs),
        '后端版本号没有写入前端版本区域');
});
check('导出区只保留一个模板下拉框', () => {
    assert.ok(/id="format-presets"/.test(html));
    assert.equal(/id="saved-templates"/.test(html), false, '保存模板不应再使用第二个下拉框');
});
check('导出格式、全部/当前规则/自定义范围与通用下载按钮已接线', () => {
    assert.ok(/id="export-format"/.test(html));
    assert.ok(/name="export-scope" value="direct"/.test(html));
    assert.ok(/name="export-scope" value="rules"/.test(html));
    assert.ok(/name="export-scope" value="custom"/.test(html));
    assert.equal(/name="export-scope" value="all"/.test(html), false);
    assert.ok(/id="btn-download"/.test(html));
    assert.equal(/id="btn-download-csv"/.test(html), false);
    assert.ok(/结果直出/.test(html) === false, '范围标签不应再叫结果直出');
    assert.ok(/export-segmented/.test(html));
    assert.ok(/export-toolbar/.test(html));
});
check('分组取前 N 已改名为自定义展示规则', () => {
    assert.ok(/自定义展示规则/.test(html));
    assert.equal(/分组取前 N/.test(html), false);
});
check('前 N 输入留空即显示无限制，不再附加注释', () => {
    assert.ok(/placeholder="无限制"/.test(quotaRulesJs));
    assert.equal(/0 表示不限制/.test(quotaRulesJs), false);
});
check('结果区右上角不再展示导出范围摘要', () => {
    assert.equal(/id="export-count"/.test(html), false);
    assert.equal(/result-summary/.test(html), false);
});
check('颜色阈值可配置字段已接线', () => {
    for (const id of ['badge-latency-green-end', 'badge-latency-yellow-end', 'badge-speed-red-end', 'badge-speed-yellow-end']) {
        assert.ok(html.includes(`id="${id}"`), `${id} 不存在`);
        assert.ok(appJs.includes(`'${id}'`), `${id} 未在 app.js 中接线`);
    }
    assert.ok(/200 ms<\/span> ≤ 延迟 ≤/.test(html));
    assert.ok(/延迟 &gt;/.test(html));
    assert.ok(/100 kB\/s<\/span> ≤ 速度 &lt;/.test(html));
    assert.ok(/速度 ≥/.test(html));
    assert.ok(/输入框编辑所在区间的右端值/.test(html));
    assert.ok(/addEventListener\('input', previewBadgeThresholdsFromUI\)/.test(appJs));
});
check('导出区使用左右布局，CSV 使用表格预览', () => {
    assert.ok(/class="export-layout"/.test(html));
    assert.ok(/class="export-config"/.test(html));
    assert.ok(/id="csv-preview"/.test(html));
    assert.ok(/id="csv-preview-head"/.test(html));
    assert.ok(/id="csv-preview-body"/.test(html));
    assert.ok(/function renderCSVPreview/.test(appJs));
    assert.ok(/csvValue\(column, result/.test(appJs), 'CSV 预览应直接读取结构化结果，不应拆分 CSV 字符串');
});
check('官方模式自动加载并只保留更新与生成按钮', () => {
    assert.equal(/id="btn-fetch-ranges"/.test(html), false);
    assert.ok(/id="btn-refresh-ranges"[^>]*>更新本地缓存</.test(html));
    assert.ok(/if \(store\.mode === 'official' && !officialRanges\) fetchRanges\(false\)/.test(appJs));
    assert.ok(/打开官方模式后自动读取本地缓存/.test(html));
});
check('导出三范围字段语义已接线', () => {
    assert.ok(/function exportColumns\(\)/.test(appJs));
    assert.ok(/if \(scope === 'direct'\) return \[\.\.\.CSV_COLUMNS\]/.test(appJs));
    assert.ok(/if \(scope === 'custom'\) return CSV_COLUMNS\.filter\(column => customColumnKeys\.includes\(column\.key\)\)/.test(appJs));
    assert.ok(/return CSV_COLUMNS\.filter\(column => visibleColumnKeys\.includes\(column\.key\)\)/.test(appJs));
    assert.ok(/if \(exportScope\(\) === 'custom'\) return customResults/.test(appJs));
    assert.equal(/customTouched/.test(appJs), false);
});
check('自定义导出仅保留勾选追加与字段选择', () => {
    for (const id of ['custom-field-picker', 'custom-field-options', 'btn-custom-fields-all', 'btn-custom-append']) {
        assert.ok(html.includes(`id="${id}"`), `${id} 不存在`);
    }
    assert.equal(/id="custom-ip-input"/.test(html), false);
    assert.equal(/id="btn-custom-add-text"/.test(html), false);
    assert.equal(/addCustomIPsFromText/.test(appJs), false);
    assert.ok(appJs.includes('function renderCustomFieldOptions()'));
});
check('反代模式输入框与候选区高度固定且对齐', () => {
    assert.ok(/resize: none;/.test(css));
    assert.ok(/\.input-pane textarea, \.input-candidate-grid > \.candidate-pane textarea \{ height: 278px; min-height: 278px; resize: none; \}/.test(css));
    assert.ok(/\.input-candidate-grid \{ display: grid; grid-template-columns: minmax\(0, 1fr\) 54px minmax\(0, 1fr\); align-items: start; gap: 7px; \}/.test(css));
    assert.ok(/\.pane-head \{ display: flex; align-items: center; justify-content: space-between; gap: 10px; min-height: 24px; margin-bottom: 6px; font-size: 12px; font-weight: 650; \}/.test(css));
    assert.equal(/class="input-feedback pane-foot"/.test(html), false);
    assert.equal(/class="candidate-stats pane-foot"/.test(html), false);
});
check('结果筛选关键词更长、延迟与速度更窄', () => {
    assert.ok(/#result-filter \{ flex: 2 1 320px; min-width: 220px; \}/.test(css));
    assert.ok(/#result-max-latency, #result-min-speed \{ flex: 1 1 140px; min-width: 120px; max-width: 200px; \}/.test(css));
});
check('IPS 检测地址在本地配置中可见', () => {
    assert.ok(/id="advanced-ips-url"/.test(html));
    assert.ok(/\$\('advanced-ips-url'\)/.test(appJs));
});
check('显示字段支持全选和单独保存', () => {
    assert.ok(/id="btn-column-all"/.test(html));
    assert.ok(/id="btn-column-save"/.test(html));
    assert.ok(/saveDisplayColumns/.test(appJs));
});
check('结果工具栏分组与面板按钮状态接线', () => {
    assert.ok(/class="result-filter-group"/.test(html));
    assert.ok(/class="result-view-controls"/.test(html));
    assert.ok(/aria-expanded/.test(html));
    assert.ok(/syncColumnToggle/.test(appJs));
});
check('远程地址支持 TXT 与 CSV', () => {
    assert.ok(/远程 TXT \/ CSV 链接/.test(html));
    assert.ok(/importCSVText/.test(appJs), '前端缺少远程 CSV 转换逻辑');
});
check('筛选说明问号按钮已接线', () => {
    assert.ok(/id="btn-filter-help"/.test(html), '缺少筛选说明按钮');
    assert.ok(/id="filter-help"/.test(html), '缺少筛选说明面板');
    assert.ok(/btn-filter-help/.test(appJs), '问号按钮未绑定事件');
});
check('模式标签具备完整 tab/tabpanel 语义与键盘导航', () => {
    assert.ok(/id="tab-proxy"[\s\S]*?aria-controls="source-proxy"[\s\S]*?tabindex="0"/.test(html));
    assert.ok(/id="tab-official"[\s\S]*?aria-controls="source-official"[\s\S]*?tabindex="-1"/.test(html));
    assert.ok(/id="source-proxy"[^>]*role="tabpanel"[^>]*aria-labelledby="tab-proxy"/.test(html));
    assert.ok(/id="source-official"[^>]*role="tabpanel"[^>]*aria-labelledby="tab-official"/.test(html));
    for (const key of ['ArrowRight', 'ArrowLeft', 'Home', 'End']) assert.ok(appJs.includes(`event.key === '${key}'`));
});
check('测速字段使用 label，进度条同步 ARIA 数值', () => {
    for (const id of ['spd-concurrency', 'spd-duration', 'spd-minspeed']) {
        assert.ok(new RegExp(`<label class="field rule-line">[\\s\\S]*?id="${id}"[\\s\\S]*?</label>`).test(html));
    }
    assert.ok(/id="progress-wrap" role="progressbar"[^>]*aria-valuenow="0"/.test(html));
    assert.ok(/setAttribute\('aria-valuenow'/.test(appJs));
});

// 静态标记里的 class 选择器。app.js 还用了三处 querySelector：
//   .mode-tab（静态）/ .field input（静态）/ .quota-condition（运行时生成）
console.log('class 选择器:');
check('.mode-tab 在静态标记里存在', () => {
    assert.ok(/class="mode-tab/.test(html), 'mode-tabs 的按钮绑定会一个都连不上');
});
check('.quota-condition 由自定义展示规则编辑器生成', () => {
    assert.ok(/className = 'quota-condition'/.test(quotaRulesJs), '组合规则缺少条件编辑行');
    assert.ok(/picker\.getSelectedInOrder\(\)/.test(quotaRulesJs), '显示规则没有按选择顺序读取多选值');
});

console.log('初次检测是否继续测速:');
check('spd-enable 位于速度条件标题行', () => {
    const panelStart = html.indexOf('<div class="rule-group" id="spd-panel">');
    const panelEndMatch = /<\/div>\r?\n\r?\n\s*<div class="rule-group">/.exec(html.slice(panelStart));
    const panelEnd = panelEndMatch ? panelStart + panelEndMatch.index : -1;
    assert.notEqual(panelStart, -1, 'spd-panel 不存在');
    assert.notEqual(panelEnd, -1, 'spd-panel 结束位置无法识别');
    const panelHtml = html.slice(panelStart, panelEnd);
    assert.ok(/class="rule-group-head panel-head"[^>]*>[\s\S]*?id="spd-enable"/.test(panelHtml),
        'spd-enable 不在标题行里');
});
// 锚点找不到时 indexOf 返回 -1，slice(-1) 会静默给出最后一个字符 —— 那样一条
// 断言可能因为「函数改名了」而假通过或假失败，两种都比不上直接炸掉锚点本身。
const bodyOf = anchor => {
    const at = appJs.indexOf(anchor);
    assert.notEqual(at, -1, `锚点 "${anchor}" 在 app.js 里找不到了（函数被改名或删了？）`);
    return appJs.slice(at);
};
const fnBody = anchor => {
    const body = bodyOf(anchor);
    const next = body.indexOf('\nfunction ', 1);
    return next > 0 ? body.slice(0, next) : body;
};

check('速度条件始终可编辑，复选框只控制是否自动继续测速', () => {
    const body = bodyOf('function applySpeedEnabled').split('\n}')[0];
    assert.equal(body.includes('input.disabled'), false);
    assert.equal(body.includes("classList.toggle('disabled'"), false);
});
check('统一最大数量按速度规则状态分配到最终阶段', () => {
    const latency = fnBody('function latencyOptions');
    const speed = fnBody('function speedOptions');
    assert.ok(/speedEnabled\(\) \? 0 : ruleMaxResults\(\)/.test(latency), '组合检测的数量限制应只统计最终达标结果');
    assert.ok(/maxResults: ruleMaxResults\(\)/.test(speed), '统一最大数量没有作用到最终测速阶段');
});

check('设置修改后自动防抖保存，刷新不再丢', () => {
    assert.ok(/function scheduleSettingsAutoSave/.test(appJs), '缺少设置自动保存调度');
    assert.ok(/function scheduleConfigAutoSave/.test(appJs), '缺少配置自动保存调度');
    assert.ok(/function flushAutoSaves/.test(appJs), '缺少自动保存冲刷');
    assert.ok(/function bindSettingsAutoSave/.test(appJs), '缺少设置页自动保存绑定');
    assert.ok(/function readConfigFields/.test(appJs), '缺少 config 字段读取');
    assert.ok(/pagehide[\s\S]{0,80}flushAutoSaves\(\{ unload: true \}\)/.test(appJs), '页面卸载前没有冲刷待保存改动');
    const rules = fnBody('function bindRulesAndRun');
    assert.ok(/scheduleSettingsAutoSave\(\)/.test(rules), '规则字段未接入自动保存');
    assert.ok(/scheduleConfigAutoSave\(\)/.test(rules), '数据源字段未接入 config 自动保存');
    const settingsBody = fnBody('function bindSettingsAutoSave');
    assert.ok(/auto-lat-concurrency/.test(settingsBody) && /auto-spd-duration/.test(settingsBody), '设置页维护参数未接入自动保存');
    assert.ok(/advanced-trace-url/.test(settingsBody) && /advanced-official-sources/.test(settingsBody), '设置页数据源字段未接入自动保存');
    assert.ok(/debugLog: \$\('log-enable'\)\.checked/.test(appJs), '整表保存会冲掉日志开关');
});
check('手动「保存到本地」按钮保留为立即保存', () => {
    assert.ok(/btn-save-settings[\s\S]{0,80}saveLocalSettings/.test(appJs), '保存到本地按钮未绑定');
    assert.ok(/async function saveLocalSettings/.test(appJs), '缺少 saveLocalSettings');
    assert.ok(/readConfigFields\(\)/.test(bodyOf('async function saveLocalSettings')), '手动保存未复用 config 字段读取');
});
check('mobile header stays on one compact row', () => {
    assert.ok(/@media \(max-width: 640px\)[\s\S]*?\.header-inner \{ display: flex; flex-wrap: nowrap; \}/.test(css));
    assert.ok(/@media \(max-width: 640px\)[\s\S]*?\.header-status \{ flex: 0 0 auto; justify-content: flex-end; margin-left: auto; flex-wrap: nowrap; \}/.test(css));
    assert.ok(/@media \(max-width: 640px\)[\s\S]*?\.local-badge \{ display: none; \}/.test(css));
});
check('official port hint distinguishes explicit and fallback ports', () => {
    const body = bodyOf('function updateDefaultPortHint').slice(0, 700);
    assert.ok(body.includes("store.mode === 'official'"));
    assert.ok(body.includes('officialSettings()'));
    assert.ok(body.includes("$('lat-tls').checked ? 443 : 80"));
});

console.log(`\n通过 ${pass} 项（引用 ${referencedIds.length} 个 id / 声明 ${idSet.size} 个）`);
