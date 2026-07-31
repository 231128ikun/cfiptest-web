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

// 静态标记里的 class 选择器。app.js 还用了三处 querySelector：
//   .mode-tab（静态）/ .field input（静态）/ #quota-grid .quota-item（运行时生成）
console.log('class 选择器:');
check('.mode-tab 在静态标记里存在', () => {
    assert.ok(/class="mode-tab/.test(html), 'mode-tabs 的按钮绑定会一个都连不上');
});
check('.quota-item 由 renderQuotaGrid 生成，且带 data-group', () => {
    // 这两个必须同时成立：btn-quota-apply 读的是 item.dataset.group
    assert.ok(/class="quota-item"[^>]*data-group=/.test(appJs), 'data-group 缺失会让配额全部落到 undefined 键上');
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
    assert.ok(/speedEnabled\(\) \? 0 : ruleMaxResults\(\)/.test(latency), '启用速度规则时延迟阶段不应先截断');
    assert.ok(/maxResults: ruleMaxResults\(\)/.test(speed), '统一最大数量没有作用到最终测速阶段');
});

console.log(`\n通过 ${pass} 项（引用 ${referencedIds.length} 个 id / 声明 ${idSet.size} 个）`);
