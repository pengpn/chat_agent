# 项目协作规范

## 前端设计系统

任何新增或修改前端页面、组件、样式前，必须依次阅读：

1. `design-system/agent-workspace/MASTER.md`
2. `design-system/agent-workspace/COMPONENTS.md`
3. 对应的 `design-system/agent-workspace/pages/*.md` 页面规范（存在时）

实现要求：

- 优先复用 `frontend/src/styles/tokens.css` 中的语义 Token，不在组件内随意新增颜色、圆角、阴影、字号或动效时长。
- 新视觉模式必须先更新设计系统文档和 Token，再实现组件。
- 保持暖白、石墨灰、深青色的克制视觉语言；禁止使用紫色 AI 渐变、霓虹光效和无意义装饰动画。
- 图标使用统一的线性 SVG，不使用 Emoji 充当功能图标。
- 交互控件必须有 hover、focus-visible、disabled 状态；移动端主要点击区域不得小于 44×44px。
- 所有新界面至少验证 375px、760px、1024px 和 1440px 宽度，并尊重 `prefers-reduced-motion`。
- 前端改动完成后运行 `cd frontend && npm test && npm run build`。

## 项目约定

- 本项目不使用 Superpowers skills 或 Superpowers plugin。
- Git 提交信息使用中文，例如 `feat: 新增模型选择功能`、`fix: 修复重复关闭按钮`。
- 新增或修改环境变量时，同步更新 `.env` 和 `.env.example`，并使用中文注释。
- 完成功能迭代后，在 `docs/` 下补充面向学习的 Markdown 文档。
