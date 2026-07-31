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
check('引用面不是空的（正则失效时这条会先炸）', () => {
    assert.ok(referencedIds.length > 60, `只解析出 ${referencedIds.length} 个引用，正则可能已失配`);
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
check('IPS 检测地址在本地配置中可见', () => {
    assert.ok(/id="advanced-ips-url"/.test(html));
    assert.ok(/\$\('advanced-ips-url'\)/.test(appJs));
});
check('显示字段支持全选和单独保存', () => {
    assert.ok(/id="btn-column-all"/.test(html));
    assert.ok(/id="btn-column-save"/.test(html));
    assert.ok(/saveDisplayColumns/.test(appJs));
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

// 静态标记里的 class 选择器。app.js 还用了三处 querySelector：
//   .mode-tab（静态）/ .field input（静态）/ .quota-condition（运行时生成）
console.log('class 选择器:');
check('.mode-tab 在静态标记里存在', () => {
    assert.ok(/class="mode-tab/.test(html), 'mode-tabs 的按钮绑定会一个都连不上');
});
check('.quota-condition 由组合规则编辑器生成', () => {
    assert.ok(/className = 'quota-condition'/.test(appJs), '组合规则缺少条件编辑行');
    assert.ok(/picker\.getSelected\(\)/.test(appJs), '组合规则没有读取多选值');
});

console.log('初次检测是否继续测速:');
check('spd-enable 位于速度条件标题行', () => {
    const panelStart = html.indexOf('<div class="rule-group" id="spd-panel">');
    const panelEnd = html.indexOf('</div>\n\n                <div class="rule-group">', panelStart);
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

check('速度条件始终可编辑，复选框只控制是否自动继续测速', () => {
    const body = bodyOf('function applySpeedEnabled').split('\n}')[0];
    assert.equal(body.includes('input.disabled'), false);
    assert.equal(body.includes("classList.toggle('disabled'"), false);
});
check('统一最大数量按速度规则状态分配到最终阶段', () => {
    const latency = bodyOf('function latencyOptions').slice(0, 700);
    const speed = bodyOf('function speedOptions').slice(0, 700);
    assert.ok(/speedEnabled\(\) \? 0 : ruleMaxResults\(\)/.test(latency), '组合检测的数量限制应只统计最终达标结果');
    assert.ok(/maxResults: ruleMaxResults\(\)/.test(speed), '统一最大数量没有作用到最终测速阶段');
});

console.log(`\n通过 ${pass} 项（引用 ${referencedIds.length} 个 id / 声明 ${idSet.size} 个）`);
