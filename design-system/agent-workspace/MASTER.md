# Agent Workspace 设计系统

> 状态：`1.0`，项目级视觉规范唯一事实来源（Single Source of Truth）。

## 1. 产品与设计方向

Agent Workspace 面向各行各业的个人与团队用户，不是只服务工程师的开发者工具。界面需要让用户感到安静、可信、清晰、有工作感，而不是刻意强调“AI 科技感”。

固定视觉关键词：

- 现代、克制、内容优先
- 暖白纸张感，而非冰冷纯白
- 石墨灰文字，而非纯黑
- 深青色品牌强调，而非紫色 AI 渐变
- 适中的信息密度和明确的工作层级

禁止：

- 紫色、蓝紫渐变作为主视觉
- 霓虹光效、玻璃拟态堆叠、强烈发光阴影
- 为了“像 AI”而增加的星星、魔法棒和无意义动画
- 同一层级混用扁平、拟物、玻璃等多套风格

## 2. 设计原则

1. **内容先于装饰**：回答、任务和上下文是视觉中心，控件保持低噪音。
2. **层级来自排版和留白**：优先使用字号、字重、间距和边框建立层级，不依赖大色块。
3. **一个区域一个主要操作**：主要按钮使用深青色，其余操作降低强调。
4. **状态必须可感知**：hover、focus、selected、disabled、loading、error 都有明确但克制的反馈。
5. **默认可访问**：键盘、屏幕阅读器、低动态偏好和移动端点击区域不是后补功能。

## 3. Token 使用规则

代码中的 Token 定义位于 `frontend/src/styles/tokens.css`。新样式必须优先使用语义变量，禁止在组件中随意增加相近色值。

### 3.1 核心颜色

| 语义 | Token | 值 | 用途 |
| --- | --- | --- | --- |
| 画布 | `--color-canvas` | `#f4f5f2` | 登录页、最外层背景 |
| 工作区 | `--color-workspace` | `#f7f8f5` | 对话主区域 |
| 普通表面 | `--color-surface` | `#fbfcfa` | 页头、弱卡片 |
| 浮层表面 | `--color-surface-raised` | `#ffffff` | 输入框、下拉菜单、Modal |
| 主文字 | `--color-text-primary` | `#1e2927` | 标题和正文 |
| 次文字 | `--color-text-secondary` | `#53635d` | 标签和说明 |
| 弱文字 | `--color-text-muted` | `#64736e` | 辅助信息，白底对比度 4.98:1 |
| 品牌主色 | `--color-primary` | `#0f766e` | 主按钮、焦点、关键链接 |
| 品牌悬停 | `--color-primary-hover` | `#0b625c` | 主操作 hover |
| 侧栏背景 | `--color-sidebar` | `#17211f` | 左侧工作区导航 |
| 危险状态 | `--color-danger` | `#a13b46` | 错误和破坏性操作 |

关键对比度：主文字/画布 13.69:1，主色/白色 5.47:1，侧栏主文字/侧栏 14.82:1，均达到 WCAG AA。

新增颜色的流程：先确认现有语义 Token 无法表达，再在本文件和 `tokens.css` 同步增加。不要以组件名命名颜色，例如禁止 `--model-picker-green`。

### 3.2 字体与字号

- 界面字体：`--font-sans`，Plus Jakarta Sans + Noto Sans SC。
- 长文本：`--font-content`，中文优先使用 Noto Sans SC。
- 模型名、Token、序列号、代码：`--font-mono`，IBM Plex Mono。
- 正文默认 14–15px，移动端输入控件 16px，避免 iOS 自动缩放。
- 9–11px 只用于非关键元数据、英文 Eyebrow 和模型能力标签，不用于正文或操作说明。
- 标题字重 600，正文 400，按钮和标签 500–600。
- 中文正文行高 1.7–1.9；界面标签行高 1.3–1.5。

### 3.3 间距

采用 4px 基础节奏，只使用 `--space-*`：4、8、12、16、20、24、32、40、48px。

- 图标与文字：6–8px
- 同组控件：8px
- 卡片内边距：12–16px
- 区块之间：20–32px
- 页面级留白：32–48px，允许使用 `clamp()` 响应式变化

### 3.4 圆角、边框与阴影

- 小控件：`--radius-sm`（5px）
- 卡片、输入框、组合控件：`--radius-md`（7px）
- 下拉菜单和浮层：`--radius-lg`（10px）
- 胶囊标签：`--radius-pill`
- 默认边框 1px，使用 `--color-border`；不要用阴影代替所有边界。
- 普通卡片使用 `--shadow-surface`，下拉浮层使用 `--shadow-dropdown`。
- 禁止大面积强阴影和多彩阴影。

### 3.5 动效

- 普通状态切换：160–180ms。
- 复杂浮层进入：不超过 240ms。
- 只动画 `opacity` 和 `transform`，避免动画 width、height、top、left。
- 进入使用 `--ease-enter`，退出使用 `--ease-exit`。
- 动效必须表达状态变化，不做持续装饰动画。
- 所有动画必须被全局 `prefers-reduced-motion: reduce` 规则降级。

### 3.6 图标

- 使用线性 SVG，默认 `stroke-width: 1.8`，`stroke-linecap/linejoin: round`。
- 同一操作层级保持相同尺寸：常用 16、18、20px。
- 图标按钮必须提供 `aria-label` 和可见 focus 状态。
- 不使用 Emoji 作为功能图标，不混用多套填充/线性风格。

## 4. 布局与响应式

固定检查宽度：375、760、1024、1440px。

- 桌面：侧栏固定 `--sidebar-width: 272px`，主内容自适应。
- 对话正文与输入区最大宽度 `--content-max-width: 850px`。
- 760px 以下进入移动布局；控件不得产生横向滚动。
- 移动端主要点击区域至少 44×44px，相邻点击目标建议间距至少 8px。
- 浮层使用固定层级：sticky 10、dropdown 40、modal 100、toast 200，禁止随意使用 9999。

## 5. 可访问性底线

- 普通文字对比度至少 4.5:1，大文字和非文字图形至少 3:1。
- 所有交互可用键盘完成，Tab 顺序与视觉顺序一致，浮层支持 Escape 关闭。
- 不删除浏览器 outline，除非提供 2px 以上等效 `focus-visible` 样式。
- 状态不能只依靠颜色；需要同时使用文字、图标或形状。
- 错误信息靠近发生位置，并使用 `role="alert"` 或合适的 `aria-live`。
- 异步按钮执行时禁用，耗时超过 300ms 时提供进度反馈。

## 6. 实现与验收流程

每次前端视觉改动：

1. 阅读本文件、`COMPONENTS.md` 和对应页面 override。
2. 优先复用现有组件模式和 Token。
3. 新模式先更新设计系统和 Token，再写组件代码。
4. 验证默认、hover、focus-visible、selected、disabled、loading、error 状态。
5. 验证 375、760、1024、1440px，并检查无横向滚动。
6. 运行 `npm test`、`npm run build` 和 `git diff --check`。

如果业务确实需要偏离规范，必须在 `pages/` 创建页面 override，记录原因、范围和替代值；页面规范只覆盖明确列出的条目，其余仍继承本文件。
