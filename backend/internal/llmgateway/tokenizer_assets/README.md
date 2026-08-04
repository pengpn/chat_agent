# DeepSeek V4 官方 Tokenizer

`deepseek_v4_tokenizer.json` 来自 DeepSeek 官方发布的
[`deepseek-ai/DeepSeek-V4-Flash`](https://huggingface.co/deepseek-ai/DeepSeek-V4-Flash)
模型仓库，下载路径为：

```text
https://huggingface.co/deepseek-ai/DeepSeek-V4-Flash/resolve/main/tokenizer.json
```

- 下载日期：2026-08-04
- SHA-256：`8f9f37ca37fdc4f5fd36d5cf4d3b0e8392edb4e894fd10cc0d70b4957c8633cf`
- 上游许可证：MIT

该文件通过 `go:embed` 编译进后端，运行时不需要访问 Hugging Face。更新模型
tokenizer 时必须同步更新哈希、官方对照测试向量和上下文 Token 管理文档。
