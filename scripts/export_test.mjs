// B4 回归：列注册表（columns.js）与导出序列化（exporter.js）。
//
// 交付文档 §5.4 把 B4 记为「代码已满足，只需验证」，但验证手段一直是人眼——
// 这个脚本把它变成可重复的门禁。两个模块都不碰 DOM（downloadAsCSV 除外，
// 用最小桩件覆盖），所以能直接在 node 里跑。
import assert from 'node:assert/strict';

const { ALL_COLUMNS, TABLE_COLUMNS, CSV_COLUMNS, GROUP_COLUMNS, csvValue, columnByKey, escapeHTML, setBadgeThresholds } =
    await import('../web/js/columns.js');

let pass = 0;
const check = (name, fn) => {
    try { fn(); pass++; console.log(`  ok  ${name}`); }
    catch (e) { console.log(`  FAIL ${name}\n       ${e.message}`); process.exitCode = 1; }
};

// 结果对象夹具：覆盖需要转义的值、null、以及不在结果上的 enableTLS。
const ROW = {
    ip: '104.16.0.1', port: 443, dataCenter: 'NRT',
    locCode: 'NRT', region: 'Asia Pacific', city: 'Tokyo', regionZh: '亚太',
    country: '日本', cityZh: '东京', emoji: '🇯🇵',
    tcpLatencyMs: 128, downloadSpeedKBs: 2048.7,
    outboundIP: '104.16.0.1', ipType: 'IPv4', ipsType: '原生',
    asn: 13335, asnOrg: 'Cloudflare, Inc.',   // 逗号 —— CSV 必须引起来
    visitScheme: 'https', tlsVersion: 'TLSv1.3', sni: 'speed.cloudflare.com',
    httpVersion: 'HTTP/2', warp: 'off', gateway: 'off', rbi: 'off',
    kex: 'X25519', timestamp: '2026-07-30T18:00:00Z',
};

console.log('列注册表一致性:');
check('key 唯一', () => {
    const keys = ALL_COLUMNS.map(c => c.key);
    assert.equal(new Set(keys).size, keys.length);
});
check('每列至少出现在一处（inTable 或 inCSV）', () => {
    const orphan = ALL_COLUMNS.filter(c => !c.inTable && !c.inCSV);
    assert.deepEqual(orphan, [], '既不在表格也不在 CSV 的列是死代码');
});
check('CSV 列数为 27（index.html 的「共 N 列」由它算出）', () => {
    assert.equal(CSV_COLUMNS.length, 27);
});
check('伪列 _sel 不进 CSV', () => {
    assert.equal(CSV_COLUMNS.some(c => c.key === '_sel'), false);
});
check('CSV label 无重复（表头重名会让用户分不清两列）', () => {
    const labels = CSV_COLUMNS.map(c => c.label);
    assert.equal(new Set(labels).size, labels.length);
});
check('CSV 顺序 = ALL_COLUMNS 顺序（导出列序是用户可见约定）', () => {
    const expected = ALL_COLUMNS.filter(c => c.inCSV).map(c => c.key);
    assert.deepEqual(CSV_COLUMNS.map(c => c.key), expected);
});
check('TABLE_COLUMNS 11 列，首列是勾选伪列', () => {
    assert.equal(TABLE_COLUMNS.length, 11);
    assert.equal(TABLE_COLUMNS[0].key, '_sel');
});
check('可排序列都不是伪列，且 number 型列声明了 type', () => {
    assert.equal(columnByKey('_sel').sortable, false);
    assert.equal(columnByKey('port').type, 'number');
    assert.equal(columnByKey('tcpLatencyMs').type, 'number');
    assert.equal(columnByKey('downloadSpeedKBs').type, 'number');
});
check('columnByKey 未知 key 返回 undefined 而非抛错', () => {
    assert.equal(columnByKey('nope'), undefined);
});
check('分组字段来自统一列注册表且不依赖当前显示列', () => {
    assert.ok(GROUP_COLUMNS.some(c => c.key === 'country'));
    assert.ok(GROUP_COLUMNS.some(c => c.key === 'asn'));
    assert.ok(GROUP_COLUMNS.some(c => c.key === 'ipsType'));
    assert.equal(new Set(GROUP_COLUMNS.map(c => c.key)).size, GROUP_COLUMNS.length);
});

console.log('csvValue 取值规则:');
check('缺省取 r[key]', () => {
    assert.equal(csvValue({ key: 'ip' }, ROW), '104.16.0.1');
});
check('enableTLS 来自 ctx，不是结果对象', () => {
    assert.equal(csvValue({ key: 'enableTLS' }, ROW, { enableTLS: false }), 'false');
    assert.equal(csvValue({ key: 'enableTLS' }, ROW, { enableTLS: true }), 'true');
});
check('延迟带 ms 单位；null 导出空串而不是 "null"', () => {
    assert.equal(csvValue({ key: 'tcpLatencyMs' }, ROW), '128 ms');
    assert.equal(csvValue({ key: 'tcpLatencyMs' }, { ...ROW, tcpLatencyMs: null }), '');
});
check('速度取整带 kB/s；未测速（0/undefined）导出空串', () => {
    assert.equal(csvValue({ key: 'downloadSpeedKBs' }, ROW), '2049 kB/s');
    assert.equal(csvValue({ key: 'downloadSpeedKBs' }, { ...ROW, downloadSpeedKBs: 0 }), '');
    assert.equal(csvValue({ key: 'downloadSpeedKBs' }, { ...ROW, downloadSpeedKBs: undefined }), '');
});
check('csvValue 不返回表格用的 HTML（render 与 csv 是两条路）', () => {
    for (const col of CSV_COLUMNS) {
        const v = String(csvValue(col, ROW, { enableTLS: true }) ?? '');
        assert.equal(v.includes('<span'), false, `${col.key} 把徽章 HTML 漏进了 CSV`);
    }
});
check('render 用于表格，返回的是带转义的 HTML', () => {
    assert.equal(columnByKey('tcpLatencyMs').render(ROW), '<span class="badge fast">128 ms</span>');
    assert.equal(columnByKey('cityZh').render({ cityZh: '<b>x</b>' }), '&lt;b&gt;x&lt;/b&gt;');
    assert.equal(columnByKey('cityZh').render({ city: 'Tokyo' }), 'Tokyo', 'cityZh 缺失时回落 city');
});
check('颜色阈值可配置，修改后立即影响徽章颜色', () => {
    setBadgeThresholds({ latencyFastMs: 50, latencyMidMs: 60, speedFastKBs: 200, speedMidKBs: 50 });
    assert.equal(columnByKey('tcpLatencyMs').render({ tcpLatencyMs: 55 }), '<span class="badge mid">55 ms</span>');
    assert.equal(columnByKey('downloadSpeedKBs').render({ downloadSpeedKBs: 100 }), '<span class="badge mid">100 kB/s</span>');
    setBadgeThresholds({});
    assert.equal(columnByKey('tcpLatencyMs').render({ tcpLatencyMs: 55 }), '<span class="badge fast">55 ms</span>');
});
check('escapeHTML 覆盖五个字符，null/undefined 变空串', () => {
    assert.equal(escapeHTML(`&<>"'`), '&amp;&lt;&gt;&quot;&#39;');
    assert.equal(escapeHTML(null), '');
    assert.equal(escapeHTML(undefined), '');
});

// exporter.js 顶层不碰 DOM（document 只在 triggerDownload 函数体里用），
// 所以可以直接 import；下面 downloadAsCSV 一节再补桩件。
const { serialize, download, downloadAsCSV, downloadAsText } = await import('../web/js/exporter.js');

console.log('serialize（序列化与投递解耦）:');
// 注意 filter 保持的是注册表顺序（ip → 延迟 → ASN），不是这里数组字面量的顺序。
const COLS3 = CSV_COLUMNS.filter(c => ['ip', 'asnOrg', 'tcpLatencyMs'].includes(c.key));

check('列选择保持注册表顺序，而非用户勾选顺序', () => {
    assert.deepEqual(COLS3.map(c => c.key), ['ip', 'tcpLatencyMs', 'asnOrg']);
});
check('首行是表头，行分隔用 CRLF', () => {
    const out = serialize([ROW], 'csv', { columns: COLS3, enableTLS: true });
    const lines = out.split('\r\n');
    assert.equal(lines.length, 2);
    assert.equal(lines[0], 'IP地址,网络延迟,ASN组织');
    assert.equal(out.includes('\n\r\n'), false, '不应有裸 \\n 混进来');
});
check('含逗号的值被引号包起来', () => {
    const out = serialize([ROW], 'csv', { columns: COLS3, enableTLS: true });
    assert.equal(out.split('\r\n')[1], '104.16.0.1,128 ms,"Cloudflare, Inc."');
});
check('值里的双引号按 CSV 规范翻倍', () => {
    const row = { ...ROW, asnOrg: 'He said "hi"' };
    const out = serialize([row], 'csv', { columns: COLS3, enableTLS: true });
    assert.equal(out.split('\r\n')[1], '104.16.0.1,128 ms,"He said ""hi"""');
});
check('值里的换行被引号保护（不会撕成两行记录）', () => {
    const row = { ...ROW, asnOrg: 'A\nB' };
    const out = serialize([row], 'csv', { columns: COLS3, enableTLS: true });
    assert.ok(out.includes('"A\nB"'));
    assert.equal(out.split('\r\n').length, 2, '嵌入换行不该增加 CRLF 分隔的记录数');
});
check('空结果只输出表头', () => {
    assert.equal(serialize([], 'csv', { columns: COLS3 }), 'IP地址,网络延迟,ASN组织');
});
check('TXT 按模板逐行替换，空结果返回空串', () => {
    assert.equal(serialize([ROW], 'txt', { template: '{ip}:{port}#{country}' }),
        '104.16.0.1:443#日本');
    assert.equal(serialize([], 'txt', { template: '{ip}:{port}' }), '');
});
check('列选择被尊重：只导出传入的列', () => {
    const out = serialize([ROW], 'csv', { columns: [{ key: 'ip', label: 'IP地址' }] });
    assert.equal(out, 'IP地址\r\n104.16.0.1');
});
check('全 27 列每行字段数与表头一致', () => {
    const out = serialize([ROW, { ...ROW, asnOrg: 'a,b', cityZh: '"q"' }], 'csv',
        { columns: CSV_COLUMNS, enableTLS: true });
    const [header, ...rows] = out.split('\r\n');
    const countFields = line => {
        let n = 1, inQ = false;
        for (let i = 0; i < line.length; i++) {
            const ch = line[i];
            if (ch === '"') { if (inQ && line[i + 1] === '"') i++; else inQ = !inQ; }
            else if (ch === ',' && !inQ) n++;
        }
        return n;
    };
    assert.equal(countFields(header), 27);
    rows.forEach((r, i) => assert.equal(countFields(r), 27, `第 ${i + 1} 行字段数不符`));
});
check('不支持的格式抛错（而不是静默产出 CSV）', () => {
    assert.throws(() => serialize([ROW], 'json', { columns: COLS3 }), /不支持的导出格式/);
});
check('serialize 不写 BOM —— BOM 属于投递层', () => {
    assert.equal(serialize([ROW], 'csv', { columns: COLS3 }).charCodeAt(0) === 0xFEFF, false);
});

console.log('投递层（downloadAsCSV / downloadAsText）:');

// 最小桩：triggerDownload 的顺序是 new Blob → createElement → click，
// 所以最近构造的 Blob 就是本次要投递的那个，click 时一并记下文件名。
const captured = [];
let pendingBlob = null;
let revoked = 0;
globalThis.Blob = class {
    constructor(parts, opts = {}) {
        this.text = parts.join('');
        this.type = opts.type || '';
        pendingBlob = this;
    }
};
globalThis.URL = { createObjectURL: () => 'blob:stub', revokeObjectURL: () => { revoked++; } };
globalThis.document = {
    createElement: () => ({
        href: '', download: '',
        click() { captured.push({ blob: pendingBlob, name: this.download }); },
        remove() {},
    }),
    body: { appendChild() {} },
};

check('CSV 带 UTF-8 BOM（Excel 打开中文不乱码）', () => {
    captured.length = 0;
    downloadAsCSV([ROW], COLS3, { enableTLS: true });
    assert.equal(captured.length, 1);
    assert.equal(captured[0].blob.text.charCodeAt(0), 0xFEFF);
    assert.equal(captured[0].blob.text.slice(1), serialize([ROW], 'csv', { columns: COLS3, enableTLS: true }));
});
check('CSV 的 MIME 与默认文件名', () => {
    captured.length = 0;
    downloadAsCSV([ROW], COLS3);
    assert.equal(captured[0].blob.type, 'text/csv;charset=utf-8');
    assert.equal(captured[0].name, 'iptest-result.csv');
});
check('文件名可覆盖', () => {
    captured.length = 0;
    downloadAsCSV([ROW], COLS3, { filename: 'custom.csv' });
    assert.equal(captured[0].name, 'custom.csv');
});
check('TXT 原样投递，MIME 为 text/plain', () => {
    captured.length = 0;
    downloadAsText('1.1.1.1:443\n2.2.2.2:443');
    assert.equal(captured[0].blob.text, '1.1.1.1:443\n2.2.2.2:443');
    assert.equal(captured[0].blob.type, 'text/plain;charset=utf-8');
    assert.equal(captured[0].name, 'iptest-result.txt');
});
check('download 可指定 MIME 与文件名（统一投递入口）', () => {
    captured.length = 0;
    download('x', 'custom.json', 'application/json');
    assert.equal(captured[0].blob.text, 'x');
    assert.equal(captured[0].blob.type, 'application/json');
    assert.equal(captured[0].name, 'custom.json');
});
check('每次下载都释放 object URL（不泄漏）', () => {
    const before = revoked;
    downloadAsText('x');
    assert.equal(revoked, before + 1);
});
check('downloadAsCSV 默认 enableTLS=true', () => {
    captured.length = 0;
    const tlsCol = CSV_COLUMNS.filter(c => c.key === 'enableTLS');
    downloadAsCSV([ROW], tlsCol);
    assert.equal(captured[0].blob.text.split('\r\n')[1], 'true');
});

console.log(`\n通过 ${pass} 项`);
