# Agent Workspace 组件规范

本文件定义组件的外观、状态和交互基线。颜色、字体、间距等数值以 `MASTER.md` 和 `frontend/src/styles/tokens.css` 为准。

## 1. 通用状态

每个交互组件至少实现：default、hover、focus-visible、disabled；有选择语义时增加 selected，有异步行为时增加 loading，有失败可能时增加 error。

- 点击区域：桌面可视尺寸允许更小，但移动端命中区域必须至少 44×44px。
- Focus：2px 深青色轮廓或 `--shadow-focus`，不能仅改变颜色。
- Disabled：使用原生 `disabled`，降低透明度并显示 `not-allowed`，不能只拦截 click。
- Loading：保持原有宽高，避免布局跳动；Spinner 仅用于真实等待。

## 2. 按钮

### Primary

- 用途：每个区域唯一的主要动作，如发送、登录、确认。
- 高度：44–48px；圆角 `--radius-sm`。
- 背景 `--color-primary`，文字 `--color-on-primary`；hover 使用 `--color-primary-hover`。
- 禁止同一区域同时出现两个相同强调度的 Primary。

### Secondary

- 白色或暖白表面，1px `--color-border`，文字 `--color-text-secondary`。
- hover 使用 `--color-surface-muted`，边框略增强。

### Ghost / Icon

- 默认透明背景，hover 才出现弱表面。
- 图标可视尺寸 16–20px，完整命中区域 44×44px。
- 只有图标时必须设置中文 `aria-label` 和 `title`（需要视觉提示时）。

### Destructive

- 使用危险色文字和浅危险表面，与普通操作保持空间分隔。
- 删除数据前使用项目统一确认对话框，不使用浏览器 `alert/confirm`。

## 3. 输入控件

- 必须有可访问名称；正式表单使用可见 label，紧凑工具栏允许 `sr-only` label。
- 默认高度 44–48px，内边距 12–14px。
- 边框 `--color-border`，focus 使用 `--color-primary` + `--shadow-focus`。
- Placeholder 只提供示例，不代替字段名称。
- 错误信息紧邻字段下方，说明原因和恢复方式。
- 搜索框如果提供自定义清除按钮，必须隐藏浏览器原生 search cancel 控件，避免两个“×”。

## 4. 选择器与下拉菜单

- 简单、无需品牌化的表单优先原生 `select`；对话输入区模型选择使用项目自定义 Model Picker。
- Trigger 高度至少 44px，显示当前值、能力标签和向下 Chevron。
- 使用 `aria-haspopup`、动态 `aria-expanded`；菜单使用合适的 listbox/option 或 menu/menuitem 语义。
- 浮层向可用空间方向展开，宽度不得超过视口，层级使用 `--z-dropdown`。
- 当前项同时使用勾选图标、背景和 `aria-selected` 表达，不能只依靠颜色。
- 点击外部、选择完成和 Escape 都应关闭。

## 5. 侧栏与对话列表

- 侧栏使用 `--color-sidebar`，不在内部混入纯黑或其他深色体系。
- 当前对话：弱深青表面 + 左侧 3px 状态条 + 高对比文字。
- Hover 和 selected 可以共用色系，但 selected 必须有额外形状标记。
- 行高至少 40px；移动端如展示侧栏，行高提升到 44px。
- 重命名使用行内表单和保存/取消图标，不使用浏览器弹窗。

## 6. 页头

- 左侧展示 Eyebrow + 页面主标题；右侧仅放当前上下文状态，如当前模型。
- 页头表面使用 `--color-surface`，底部 1px 分隔线。
- 标题必须单行省略并保留可访问全名；小屏优先保证标题和主要操作可见。

## 7. 消息与 Markdown

- Assistant 内容不包裹大气泡，以正文阅读体验为主。
- User 内容使用白色弱边框气泡，最大宽度桌面 88%，移动端 100%。
- 正文使用 `--font-content`，15px，行高约 1.9。
- Markdown 标题、列表、引用、表格保持一致的垂直节奏。
- 代码块使用深石墨表面；工具栏高度至少 44px，复制按钮必须有成功/失败反馈。
- 思考内容使用可折叠弱青色区域，不与最终回答使用相同层级。

## 8. 对话输入器 Composer

- 最大宽度 850px，白色表面、1px 边框、轻阴影；focus-within 只轻微强化。
- 文本输入始终是视觉主角，附件、模型、发送操作位于底部工具栏。
- 左侧工具按“附件 → 模型”排列，右侧放唯一 Primary“发送”。
- Enter 发送、Shift+Enter 换行；中文输入法组合期间不得触发发送。
- 流式生成时发送按钮替换为停止按钮，布局宽度尽量稳定。

## 9. 附件

- 待发送附件使用紧凑 Chip，显示文件名、类型/大小和状态。
- 上传、解析、失败、重试状态必须可读，不只依赖颜色。
- 移除按钮命中区域 44px，并带包含文件名的 `aria-label`。
- 拖拽覆盖层只在拖拽期间出现，不遮挡长期内容。

## 10. 反馈组件

- Success：`--color-success` + `--color-success-surface`。
- Warning：`--color-warning` + `--color-warning-surface`。
- Error：`--color-danger` + `--color-danger-surface`。
- Toast 不抢夺焦点，普通通知使用 `aria-live="polite"`；错误使用 `role="alert"`。
- 空状态必须说明下一步；加载超过 300ms 展示 Spinner/Skeleton；失败提供重试或修复路径。

## 11. Modal、Popover 与确认框

- 表面使用白色、`--radius-lg`、`--shadow-dropdown` 或更高一级统一阴影。
- Modal 层级 `--z-modal`，Popover/Dropdown 层级 `--z-dropdown`。
- 打开后聚焦第一个合理控件；关闭后焦点返回触发器。
- 支持 Escape 和明确的关闭按钮；有未保存内容时关闭需确认。
- 禁止新代码调用 `alert()`、`confirm()`、`prompt()`。

## 12. 新组件检查表

- 是否能由现有组件组合完成？
- 是否只使用语义 Token？
- 是否实现所有必要状态？
- 是否有正确 HTML 语义和 ARIA？
- 是否支持键盘和 Escape 路径？
- 是否满足 44px 移动端点击区域？
- 是否在 375–1440px 无溢出？
- 是否尊重 reduced motion？
