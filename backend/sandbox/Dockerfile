FROM alpine:3.22

# 只在镜像构建时安装基础工具，模型运行期间不开放网络或包管理权限。
RUN apk add --no-cache bash git ripgrep python3

USER 65532:65532
WORKDIR /workspace
ENV HOME=/tmp TMPDIR=/tmp
CMD ["/bin/sh"]
