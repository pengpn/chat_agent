<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import {
  createKnowledgeBase,
  deleteKnowledgeBase,
  deleteKnowledgeDocument,
  listKnowledgeBases,
  listKnowledgeDocuments,
  reindexKnowledgeDocument,
  retryKnowledgeDocument,
  updateKnowledgeBase,
  uploadKnowledgeDocument,
} from '@/lib/api'
import { documentIsProcessing, documentStatusLabel } from '@/lib/knowledge-ui'
import type { KnowledgeBase, KnowledgeDocument } from '@/types'

const emit = defineEmits<{
  back: []
  changed: []
}>()

const bases = ref<KnowledgeBase[]>([])
const documents = ref<KnowledgeDocument[]>([])
const selectedID = ref<string | null>(null)
const loadingBases = ref(true)
const loadingDocuments = ref(false)
const basesError = ref('')
const documentsError = ref('')
const actionError = ref('')
const notice = ref('')
const creating = ref(false)
const nameDraft = ref('')
const descriptionDraft = ref('')
const editingID = ref<string | null>(null)
const renameDraft = ref('')
const renameDescription = ref('')
const saving = ref(false)
const confirmDeleteBase = ref(false)
const confirmDeleteDocumentID = ref<string | null>(null)
const uploads = ref<{ id: string; name: string; progress: number }[]>([])
const busyDocumentID = ref<string | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)
let pollTimer: ReturnType<typeof setInterval> | null = null

const selected = computed(() => bases.value.find((item) => item.id === selectedID.value) ?? null)
const processing = computed(() => documents.value.some((item) => documentIsProcessing(item.status)))

watch(selectedID, () => {
  confirmDeleteBase.value = false
  confirmDeleteDocumentID.value = null
  actionError.value = ''
  notice.value = ''
  void loadDocuments()
})

watch(processing, (active) => {
  if (pollTimer) clearInterval(pollTimer)
  pollTimer = null
  if (!active || !selectedID.value) return
  const baseID = selectedID.value
  pollTimer = setInterval(() => {
    if (selectedID.value === baseID) void loadDocuments(true)
  }, 2000)
})

onBeforeUnmount(() => {
  if (pollTimer) clearInterval(pollTimer)
})

void loadBases()

async function loadBases() {
  loadingBases.value = true
  basesError.value = ''
  try {
    bases.value = await listKnowledgeBases()
    if (!selectedID.value || !bases.value.some((item) => item.id === selectedID.value)) {
      selectedID.value = bases.value[0]?.id ?? null
    }
    emit('changed')
  } catch (cause) {
    basesError.value = cause instanceof Error ? cause.message : '知识库加载失败'
  } finally {
    loadingBases.value = false
  }
}

async function loadDocuments(silent = false) {
  const baseID = selectedID.value
  if (!baseID) {
    documents.value = []
    return
  }
  if (!silent) loadingDocuments.value = true
  documentsError.value = ''
  try {
    const items = await listKnowledgeDocuments(baseID)
    if (selectedID.value === baseID) documents.value = items
  } catch (cause) {
    if (selectedID.value === baseID) documentsError.value = cause instanceof Error ? cause.message : '文档加载失败'
  } finally {
    if (selectedID.value === baseID) loadingDocuments.value = false
  }
}

async function createBase() {
  const name = nameDraft.value.trim()
  if (!name || creating.value) return
  creating.value = true
  actionError.value = ''
  try {
    const created = await createKnowledgeBase(name, descriptionDraft.value.trim())
    nameDraft.value = ''
    descriptionDraft.value = ''
    await loadBases()
    selectedID.value = created.id
  } catch (cause) {
    actionError.value = cause instanceof Error ? cause.message : '创建失败'
  } finally {
    creating.value = false
  }
}

function startRename(base: KnowledgeBase) {
  editingID.value = base.id
  renameDraft.value = base.name
  renameDescription.value = base.description
  actionError.value = ''
}

async function saveRename(base: KnowledgeBase) {
  const name = renameDraft.value.trim()
  if (!name || saving.value) return
  saving.value = true
  actionError.value = ''
  try {
    await updateKnowledgeBase(base.id, name, renameDescription.value.trim())
    editingID.value = null
    await loadBases()
  } catch (cause) {
    actionError.value = cause instanceof Error ? cause.message : '重命名失败'
  } finally {
    saving.value = false
  }
}

async function removeBase() {
  const base = selected.value
  if (!base || saving.value) return
  saving.value = true
  actionError.value = ''
  try {
    await deleteKnowledgeBase(base.id)
    confirmDeleteBase.value = false
    selectedID.value = null
    await loadBases()
  } catch (cause) {
    actionError.value = cause instanceof Error ? cause.message : '删除失败'
  } finally {
    saving.value = false
  }
}

async function addFiles(files: File[]) {
  const baseID = selectedID.value
  if (!baseID) return
  actionError.value = ''
  notice.value = ''
  for (const file of files) {
    const upload = { id: crypto.randomUUID(), name: file.name, progress: 0 }
    uploads.value.push(upload)
    try {
      const result = await uploadKnowledgeDocument(baseID, file, (ratio) => {
        upload.progress = ratio
      })
      if (!result.created) notice.value = `「${result.document.name}」已经在这个知识库里，没有重复添加。`
    } catch (cause) {
      actionError.value = cause instanceof Error ? cause.message : `「${file.name}」上传失败`
    } finally {
      uploads.value = uploads.value.filter((item) => item.id !== upload.id)
    }
  }
  if (selectedID.value === baseID) await loadDocuments()
}

function chooseFiles(event: Event) {
  const input = event.target as HTMLInputElement
  void addFiles(Array.from(input.files ?? []))
  input.value = ''
}

async function retry(document: KnowledgeDocument) {
  if (!selectedID.value || busyDocumentID.value) return
  busyDocumentID.value = document.id
  actionError.value = ''
  try {
    const updated = await retryKnowledgeDocument(selectedID.value, document.id)
    documents.value = documents.value.map((item) => item.id === updated.id ? updated : item)
  } catch (cause) {
    actionError.value = cause instanceof Error ? cause.message : '重试失败'
  } finally {
    busyDocumentID.value = null
  }
}

async function reindex(document: KnowledgeDocument) {
  if (!selectedID.value || busyDocumentID.value) return
  busyDocumentID.value = document.id
  actionError.value = ''
  try {
    const updated = await reindexKnowledgeDocument(selectedID.value, document.id)
    documents.value = documents.value.map((item) => item.id === updated.id ? updated : item)
  } catch (cause) {
    actionError.value = cause instanceof Error ? cause.message : '重新索引失败'
  } finally {
    busyDocumentID.value = null
  }
}

async function removeDocument(document: KnowledgeDocument) {
  if (!selectedID.value || saving.value) return
  saving.value = true
  actionError.value = ''
  try {
    await deleteKnowledgeDocument(selectedID.value, document.id)
    confirmDeleteDocumentID.value = null
    documents.value = documents.value.filter((item) => item.id !== document.id)
  } catch (cause) {
    actionError.value = cause instanceof Error ? cause.message : '删除文档失败'
  } finally {
    saving.value = false
  }
}

function formatSize(bytes: number) {
  return bytes < 1024 * 1024 ? `${Math.ceil(bytes / 1024)} KB` : `${(bytes / 1024 / 1024).toFixed(1)} MB`
}
</script>

<template>
  <main class="knowledge-main">
    <header class="chat-header">
      <div>
        <p class="eyebrow">KNOWLEDGE</p>
        <h2>知识库</h2>
      </div>
      <button class="secondary-button mobile-back" type="button" @click="emit('back')">返回对话</button>
    </header>

    <div class="knowledge-layout">
      <section class="knowledge-list" aria-label="知识库列表">
        <form class="knowledge-create" @submit.prevent="createBase">
          <label for="knowledge-name">新建知识库</label>
          <input id="knowledge-name" v-model="nameDraft" maxlength="120" placeholder="例如：产品制度" :disabled="creating" />
          <label class="sr-only" for="knowledge-description">描述</label>
          <input id="knowledge-description" v-model="descriptionDraft" maxlength="2000" placeholder="可选说明，帮助自己区分资料范围" :disabled="creating" />
          <button class="send-button" type="submit" :disabled="creating || !nameDraft.trim()">{{ creating ? '正在创建…' : '创建' }}</button>
        </form>
        <p v-if="loadingBases" class="citation-state" role="status"><span class="loading-mark" />正在加载知识库…</p>
        <p v-else-if="basesError" class="citation-state citation-state-error" role="alert">{{ basesError }} <button type="button" class="text-action" @click="loadBases">重试</button></p>
        <p v-else-if="!bases.length" class="knowledge-empty">还没有知识库。创建一个后，把 TXT、PDF 或 Word 放进去，对话时就可以选择它作为依据。</p>
        <button
          v-for="base in bases"
          :key="base.id"
          type="button"
          class="knowledge-base-item"
          :class="{ active: base.id === selectedID }"
          :aria-current="base.id === selectedID ? 'true' : undefined"
          @click="selectedID = base.id"
        >
          <strong>{{ base.name }}</strong>
          <span>{{ base.description || '未填写说明' }}</span>
        </button>
      </section>

      <section class="knowledge-documents" aria-live="polite">
        <p v-if="!selected" class="knowledge-empty">选择左侧知识库后，可以上传和管理文档。</p>
        <template v-else>
          <div class="knowledge-document-header">
            <div>
              <h3>{{ selected.name }}</h3>
              <p>{{ selected.description || '上传完成后，对话里选择这个知识库，回答就会标明依据来自哪一份资料。' }}</p>
            </div>
            <div class="knowledge-document-actions">
              <button v-if="editingID !== selected.id" type="button" class="secondary-button" @click="startRename(selected)">重命名</button>
              <button v-if="!confirmDeleteBase" type="button" class="danger-button" @click="confirmDeleteBase = true">删除</button>
            </div>
          </div>
          <form v-if="editingID === selected.id" class="knowledge-rename" @submit.prevent="saveRename(selected)">
            <label :for="`rename-${selected.id}`">知识库名称</label>
            <input :id="`rename-${selected.id}`" v-model="renameDraft" maxlength="120" :disabled="saving" />
            <label :for="`rename-desc-${selected.id}`">说明</label>
            <input :id="`rename-desc-${selected.id}`" v-model="renameDescription" maxlength="2000" :disabled="saving" />
            <div>
              <button class="send-button" type="submit" :disabled="saving || !renameDraft.trim()">保存</button>
              <button class="secondary-button" type="button" :disabled="saving" @click="editingID = null">取消</button>
            </div>
          </form>
          <div v-if="confirmDeleteBase" class="inline-confirm" role="group" aria-label="确认删除知识库">
            <span>删除后不能再被新的对话检索。已经生成的回答仍会保留当时的引用片段。</span>
            <button type="button" :disabled="saving" @click="removeBase">确认删除</button>
            <button type="button" :disabled="saving" @click="confirmDeleteBase = false">取消</button>
          </div>

          <div class="knowledge-upload">
            <input ref="fileInput" class="visually-hidden" type="file" multiple accept=".txt,.pdf,.docx,text/plain,application/pdf,application/vnd.openxmlformats-officedocument.wordprocessingml.document" @change="chooseFiles" />
            <button class="secondary-button" type="button" @click="fileInput?.click()">上传文档</button>
            <p>支持 TXT、PDF 和 Word。上传后会依次读取内容、整理段落并准备检索。</p>
          </div>
          <ul v-if="uploads.length" class="knowledge-upload-list" aria-label="上传进度">
            <li v-for="item in uploads" :key="item.id">
              <span>{{ item.name }}</span>
              <progress :value="item.progress" max="1" />
              <small>{{ Math.round(item.progress * 100) }}%</small>
            </li>
          </ul>
          <p v-if="notice" class="account-inline-status" role="status">{{ notice }}</p>
          <p v-if="actionError" class="citation-state-error" role="alert">{{ actionError }}</p>
          <p v-if="loadingDocuments" class="citation-state" role="status"><span class="loading-mark" />正在加载文档…</p>
          <p v-else-if="documentsError" class="citation-state citation-state-error" role="alert">{{ documentsError }} <button type="button" class="text-action" @click="loadDocuments()">重试</button></p>
          <p v-else-if="!documents.length && !uploads.length" class="knowledge-empty">这个知识库还没有文档。上传一份资料后，处理完成就可以在对话里引用。</p>
          <ul v-else class="document-list">
            <li v-for="document in documents" :key="document.id" class="document-row" :class="document.status">
              <div>
                <strong>{{ document.name }}</strong>
                <span>{{ formatSize(document.size) }} · {{ documentStatusLabel(document.status) }}<template v-if="document.status === 'ready'"> · {{ document.chunkCount }} 个片段</template></span>
                <span v-if="document.status === 'failed'" class="document-error">{{ document.errorMessage || '处理失败，可以重试。' }}</span>
              </div>
              <div class="document-row-actions">
                <button v-if="document.status === 'failed'" type="button" class="secondary-button" :disabled="busyDocumentID === document.id" @click="retry(document)">重试</button>
                <button v-if="document.status === 'ready'" type="button" class="secondary-button" :disabled="busyDocumentID === document.id" @click="reindex(document)">重新索引</button>
                <button v-if="confirmDeleteDocumentID !== document.id" type="button" class="text-action" @click="confirmDeleteDocumentID = document.id">删除</button>
              </div>
              <div v-if="confirmDeleteDocumentID === document.id" class="inline-confirm">
                <span>删除「{{ document.name }}」后，新的回答不再使用它。</span>
                <button type="button" :disabled="saving" @click="removeDocument(document)">确认删除</button>
                <button type="button" :disabled="saving" @click="confirmDeleteDocumentID = null">取消</button>
              </div>
            </li>
          </ul>
        </template>
      </section>
    </div>
  </main>
</template>
