// ui-static-check.mjs —— 前端静态验证：把假 state 喂给 app.js 的渲染函数，断言 HTML 输出。
//
// 为什么这样做（而不是起真实例）：
//   - 仓库无 JS 测试设施、也无浏览器环境；起真实例会连上游（签到/保活有副作用）；
//   - 我们只关心「函数拿到 state 会不会渲染出该有的 DOM」，纯函数级验证足够；
//   - app.js 顶层有 `let state = null`，故用「源码改写注入」把假 state 塞进去。
//
// 用法：node ui-static-check.mjs
import { readFileSync } from "node:fs";

const raw = readFileSync(new URL("../cmd/wild-view/web/app.js", import.meta.url), "utf8");
const html = readFileSync(new URL("../cmd/wild-view/web/index.html", import.meta.url), "utf8");

// 把 `let state = null;` 改成假数据，并暴露待测函数（app.js 是脚本，函数是顶层声明）。
const FAKE_STATE = {
  version: "2.5.5",
  accounts: [
    {
      uid: "1606848467182378",
      group: "traework",
      nickname: "中招号",
      credits: 2050,
      expiring_credits: 0,
      unusable_credits: 0,
      cooling: true,
      until: "10-03 09:12",
      reason: "stream business error: solo error code=3004 …",
      disabled: false,
      last_checkin_at: "2026-10-02 09:00",
      last_checkin_ok: true,
    },
    {
      uid: "4298452267967801",
      group: "traework",
      nickname: "健康号",
      credits: 2686,
      expiring_credits: 100,
      unusable_credits: 0,
      cooling: false,
      until: "",
      reason: "",
      disabled: false,
      last_checkin_at: "2026-10-02 09:00",
      last_checkin_ok: true,
    },
  ],
  checkin_times: [],
  next_checkin: "-",
};

// ---- 最小 DOM stub ----
const nodes = {};
function mkNode(id) {
  const cls = new Set();
  return {
    id,
    innerHTML: "",
    textContent: "",
    value: "",
    hidden: true, // 真 DOM 里未声明的元素 hidden === false，但本 stub 只服务 #timeEditor（带 hidden 属性）
    disabled: false,
    style: {},
    focus: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    querySelectorAll: () => [],
    querySelector: () => mkNode(id + "-sub"),
    contains: () => false,
    appendChild: () => {},
    classList: {
      add: (c) => cls.add(c),
      remove: (c) => cls.delete(c),
      contains: (c) => cls.has(c),
      _set: cls,
    },
    onmouseenter: null,
    onmouseleave: null,
  };
}
const document = {
  getElementById: (id) => (nodes[id] ||= mkNode(id)),
  querySelectorAll: () => [],
  addEventListener: () => {},
};
const window = { addEventListener: () => {}, location: { search: "" } };

// 源码改写：注入 state + 导出函数
const patched =
  raw.replace(/let state = null;/, "let state = " + JSON.stringify(FAKE_STATE) + ";") +
  "\n; globalThis.__T = { renderAccounts, renderSummary, renderFees, refreshFees, showOpenLink, hideOpenLink, toHalfWidthAscii, addTime, commitTime, closeTimeEditor, delTime, saveTimes, loadState, setState: (s) => { state = s; } };";

// fetch 记录桩：checkin_times 变更要能断言「没点确定就不发请求」。
// POST /api/config/checkin_times 会「落库」，后续 /api/state 必须回新值——
// 不模拟这一步，app.js 的重复添加保护就会因读到旧 state 而漏检。
const fetchLog = [];
let storedTimes = FAKE_STATE.checkin_times.slice();
let fetchImpl = null;
const fetchStub = async (path, opts) => {
  fetchLog.push({ path, body: opts && opts.body });
  if (path === "/api/state") {
    const st = JSON.parse(JSON.stringify(FAKE_STATE));
    st.checkin_times = storedTimes.slice();
    return { ok: true, json: async () => st };
  }
  if (path === "/api/config/checkin_times" && opts && opts.body) {
    storedTimes = JSON.parse(opts.body).times || [];
  }
  return { ok: true, json: async () => ({ ok: true }) };
};
fetchImpl = fetchStub;

new Function("document", "window", "localStorage", "fetch", "location", "navigator", "setTimeout", "globalThis", patched)(
  document,
  window,
  { getItem: () => null, setItem: () => {} },
  (...args) => fetchImpl(...args),
  window.location,
  { userAgent: "node" },
  (fn) => { fn(); return 0; },
  globalThis
);

const T = globalThis.__T;
const results = [];
const check = (name, pass, extra = "") => results.push([name, pass, extra]);

// 费率表：空目录、单行和双列均应渲染，模型信息须安全转义。
T.renderFees({ channels: [] });
check("空费率目录可渲染", document.getElementById("feesBox").innerHTML.includes("note"));
T.renderFees({ channels: [{ channel: "traework", models: [
  { model: 'one"<', id: 'traework/one"<', priced: true, rate: 1.25 },
  { model: "two", priced: false },
  { model: "three", priced: true, free: true },
] }] });
const feeHTML = document.getElementById("feesBox").innerHTML;
check("费率表渲染多模型", feeHTML.includes("two") && feeHTML.includes("three") && feeHTML.includes("x1.25") && feeHTML.includes("unknown"));
check("模型提示安全转义", feeHTML.includes("one&quot;&lt;") && !feeHTML.includes('one"<'));
T.renderFees({ channels: [{ channel: "traework", models: [{ model: "single", priced: false }] }] });
check("费率表渲染单模型", document.getElementById("feesBox").innerHTML.includes("single"));

// 手动刷新必须等待后台任务号完成，不能 POST 后立即读取旧缓存。
fetchLog.length = 0;
let feesTask = 0;
let feesDone = 0;
let feesRunning = false;
fetchImpl = async (path, opts) => {
  const method = opts?.method || "GET";
  fetchLog.push({ path, method });
  if (path === "/api/fees/refresh") {
    feesTask++; feesRunning = true;
    return { ok: true, json: async () => ({ ok: true, task_id: feesTask }) };
  }
  if (path === "/api/fees") {
    if (feesRunning && feesTask > feesDone) feesDone = feesTask;
    feesRunning = false;
    return { ok: true, json: async () => ({ channels: [], refresh_running: feesRunning, refresh_done: feesDone }) };
  }
  return fetchStub(path, opts);
};
await T.refreshFees();
const feePosts = fetchLog.filter((f) => f.path === "/api/fees/refresh").length;
const feeGets = fetchLog.filter((f) => f.path === "/api/fees" && f.method === "GET").length;
check("手动刷新只提交一个后台任务", feePosts === 1, `实际 ${feePosts} 次`);
check("手动刷新会等待任务完成再读取", feeGets >= 1 && document.getElementById("btnRefreshFees").disabled === false);
fetchImpl = fetchStub;

// ---- 场景：授权页打不开时的兜底链接（2026-10-30 千问办公「点了没页面」）----
const openLink = document.getElementById("lcOpenLink");
openLink.classList.add("hidden"); // index.html 里该元素默认隐藏
T.showOpenLink("https://example/oauth2/auth");
check("兜底链接指向授权地址", openLink.href === "https://example/oauth2/auth" && !openLink.classList.contains("hidden"));
T.hideOpenLink();
check("后端已打开时收起兜底链接", openLink.classList.contains("hidden"));
check("前端按后端 open_mode 决定是否自己打开", raw.includes("r.open_mode") && /if \(!w\)/.test(raw));

// ---- 场景：管理密码输入框只留半角 ASCII（输入法状态不该影响密码）----
check("全角数字字母自动折半角", T.toHalfWidthAscii("中文abc１２３") === "abc123");
check("全角符号折半角", T.toHalfWidthAscii("ｐａｓｓ！＠") === "pass!@");
check("保留 ASCII 符号与空格", T.toHalfWidthAscii("P@ss word-1") === "P@ss word-1");
check("纯中文被清空", T.toHalfWidthAscii("中文密码") === "");
check("设置框接了输入阶段规整", raw.includes('asciiOnlyInput($("adminPassInput")'));


// ---- 场景 1：账号卡片渲染（查看器不再展示路由冷却/停用状态） ----
T.renderAccounts();
const cards = document.getElementById("acctList").innerHTML;
check("账号卡片已渲染", cards.length > 0);
check("健康号仍在（未被过滤）", cards.includes("健康号"));
check("不再渲染路由冷却标签", !/冷却至/.test(cards));
check("普通账号仍可删除改名", cards.includes("removeAcct(") && cards.includes("openRename("));
T.setState(FAKE_STATE);

// ---- 场景 2：渠道汇总条 ----
T.renderSummary();
const sum = document.getElementById("creditSummary").innerHTML;
check("汇总条渲染了 traework", sum.includes("TraeWork"));
check("汇总可用 = 2050+2686 = 4736", sum.includes("4,736"), sum.slice(0, 160));
check("汇总临期 = 100", sum.includes("临期100"));
check("汇总含合计", sum.includes("合计"));

// ---- 场景 3：签到时间「手动选」（2026-10-21 用户报「添加没法手动选」）----
// 旧实现：点「＋」直接塞入当前时刻，想要别的时间只能先加再删。现改为就地展开
// <input type="time">，选完点「确定」才写盘；取消/未选不发任何请求。
const POSTS = () => fetchLog.filter((f) => f.path === "/api/config/checkin_times");
fetchLog.length = 0;

T.addTime(); // 展开编辑器
check("点＋展开时间选择器", document.getElementById("timeEditor").hidden === false);
check("展开后不发请求（未选就先不发）", POSTS().length === 0);
check(
  "默认值是当前时刻（HH:MM）",
  /^\d{2}:\d{2}$/.test(document.getElementById("timePicker").value)
);

// 取消 → 收起且不发请求
T.closeTimeEditor();
check("取消后收起", document.getElementById("timeEditor").hidden === true);
check("取消不发请求", POSTS().length === 0);

// 选一个与「现在」不同的合法时间 → 确定后才 POST，且带的是手选值
document.getElementById("timePicker").value = "21:30";
T.commitTime();
check("确定后才发 POST", POSTS().length === 1, `实际 ${POSTS().length} 次`);
check(
  "提交的是手选值 21:30（不是当前时刻）",
  POSTS()[0] && POSTS()[0].body.includes("21:30"),
  POSTS()[0] && POSTS()[0].body
);
check("提交后收起编辑器", document.getElementById("timeEditor").hidden === true);

// 等 saveTimes 的异步链（POST → toast → loadState → render）跑完再验重复添加
const tick = () => new Promise((r) => setTimeout(r, 0));
for (let i = 0; i < 5; i++) await tick();

// 重复时间不再重复添加
fetchLog.length = 0;
T.addTime();
document.getElementById("timePicker").value = "21:30";
T.commitTime();
check("重复时间不发请求", POSTS().length === 0, `实际 ${POSTS().length} 次`);

// ---- 场景 4：app.js 引用的每个 id 都必须在 index.html 里存在 ----
// 起因：新增「签到时间选择器」时若只在 app.js 里取 $("timePicker") 而忘了在
// index.html 加元素，运行期会 `$(...).value` 报 null 错、整个设置弹层点开即崩，
// 而 node --check 与功能断言都发现不了（stub 会给任何 id 凭空造节点）。
const htmlIds = new Set([...html.matchAll(/id="([A-Za-z0-9_-]+)"/g)].map((m) => m[1]));
const jsIds = new Set([...raw.matchAll(/\$\("([A-Za-z0-9_-]+)"\)/g)].map((m) => m[1]));
const missingIds = [...jsIds].filter((i) => !htmlIds.has(i) && i !== "creditTip"); // creditTip 由 app.js 运行期动态创建（219 行）
check(`app.js 引用的 id 都在 index.html（漏 ${missingIds.join(", ") || "无"}）`, missingIds.length === 0, missingIds.join(", "));
check("id 解析下限守门（js ≥100 个）", jsIds.size >= 100, `实际 ${jsIds.size}`);
check("index.html 元素保底（≥100 个 id）", htmlIds.size >= 100, `实际 ${htmlIds.size}`);

// ---- 输出 ----
let failed = 0;
for (const [name, pass, extra] of results) {
  console.log(`  ${pass ? "✅" : "❌"} ${name}${extra && !pass ? "  → " + extra : ""}`);
  if (!pass) failed++;
}

// ---- 静态结构断言：同名顶层函数/常量不得重复定义 ----
// 起因（2026-10-03 复核发现）：renderSummary 曾被定义两次，前者成死代码；
// node --check 与功能断言都发现不了（后者只测生效的那版）。
// 一旦有人"修好"死的那版，面板数字会静默变化。
const uniq = [
  ["function renderSummary", /^\s*function renderSummary\s*\(/gm],
  ["const CH_ORDER", /^\s*const CH_ORDER\s*=/gm],
  ["const CH_SUMMARY_ORDER", /^\s*const CH_SUMMARY_ORDER\s*=/gm],
  ["function renderAccounts", /^\s*function renderAccounts\s*\(/gm],
  ["function render()", /^\s*function render\s*\(/gm],
];
for (const [label, re] of uniq) {
  const n = (raw.match(re) || []).length;
  // 判定「不得 >1」而非「必须 ==1」：某些定义（如 CH_ORDER）本就不该存在，
  // 删掉后是 0 次也正常；这里防的是"同一提交里写了两版"这种非预期重复。
  const pass = n <= 1;
  console.log(`  ${pass ? "✅" : "❌"} 顶层定义不重复：${label}（${n} 次）`);
  if (!pass) failed++;
}

console.log(failed === 0 ? "\n全部通过" : `\n${failed} 项失败`);
process.exit(failed === 0 ? 0 : 1);
