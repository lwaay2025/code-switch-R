<template>
  <div class="capture-page">
    <!-- Header -->
    <div class="capture-header">
      <div class="capture-header-left">
        <h1 class="capture-title">抓包监控</h1>
        <label class="toggle-wrapper">
          <input type="checkbox" v-model="enabled" @change="toggleEnabled">
          <span class="toggle-slider"></span>
          <span class="toggle-label">开启抓包</span>
        </label>
      </div>
      <div class="capture-header-right">
        <button class="capture-settings-btn" @click="showSettings = !showSettings">⚙️</button>
      </div>
    </div>

    <!-- Main: Three-Panel Layout -->
    <div class="capture-main">
      <!-- Left: Activities -->
      <div class="capture-panel activity-panel">
        <div class="panel-header"><span>Activities</span> <span class="panel-count">({{ activities.length }})</span></div>
        <div class="panel-body">
          <div v-for="act in activities" :key="act.activity_id" class="activity-item"
               :class="{ selected: selectedActivity?.activity_id === act.activity_id }"
               @click="selectActivity(act)">
            <button class="delete-btn" @click.stop="deleteActivity(act)" title="删除此 Activity">✕</button>
            <div class="activity-source">{{ act.source }}</div>
            <div class="activity-meta">
              <span>{{ act.turn_count }} turns</span>
              <span class="time">{{ fmtTime(act.last_seen_at) }}</span>
            </div>
          </div>
          <div v-if="activities.length === 0" class="empty-state">暂无数据</div>
        </div>
      </div>

      <!-- Center: Turns -->
      <div class="capture-panel turn-panel">
        <div class="panel-header"><span>Turns</span> <span class="panel-count">({{ turns.length }})</span></div>
        <div class="panel-body">
          <div v-for="turn in turns" :key="turn.turn_id" class="turn-item"
               :class="{ selected: selectedTurn?.turn_id === turn.turn_id }"
               @click="selectTurn(turn)">
            <div class="turn-model">{{ turn.model || '—' }}</div>
            <div class="turn-meta">
              <span :class="['status-tag', turn.status]">{{ turn.status }}</span>
              <span>{{ turn.total_duration_ms }}ms</span>
              <span>{{ turn.chunk_count }} chunks</span>
            </div>
          </div>
          <div v-if="turns.length === 0" class="empty-state">选择 Activity 查看</div>
        </div>
      </div>

      <!-- Right: Detail -->
      <div class="capture-panel detail-panel" v-if="selectedTurn">
        <div class="panel-header">
          <span>请求详情</span>
          <button class="export-btn" @click="doExport">导出JSON</button>
        </div>
        <div class="panel-body detail-body">
          <div v-for="req in requests" :key="req.request_id" class="request-card">
            <div class="request-summary">
              <span class="method-tag">{{ req.method }}</span>
              <span class="url-text">{{ req.url }}</span>
              <span class="status-code">{{ req.response_status_code }}</span>
            </div>
            <details class="detail-section">
              <summary>请求头</summary>
              <pre class="code-block">{{ fmtJSON(req.request_headers) }}</pre>
            </details>
            <details class="detail-section" v-if="req.request_body">
              <summary>请求体</summary>
              <pre class="code-block">{{ fmtJSON(req.request_body) }}</pre>
            </details>
            <details class="detail-section">
              <summary>响应头</summary>
              <pre class="code-block">{{ fmtJSON(req.response_headers) }}</pre>
            </details>
            <div class="response-summary">
              <span>状态: {{ req.response_status_code }}</span>
              <span>耗时: {{ selectedTurn.total_duration_ms }}ms</span>
              <span>Chunks: {{ selectedTurn.chunk_count }}</span>
              <span>字节: {{ fmtBytes(selectedTurn.total_bytes) }}</span>
            </div>
            <div class="chunks-section">
              <div class="chunks-header">Chunks ({{ chunks.length }})</div>
              <div class="chunks-list">
                <div v-for="chunk in chunks" :key="chunk.id" class="chunk-item">
                  <span class="chunk-index">#{{ chunk.chunk_index }}</span>
                  <span :class="['chunk-type', chunk.chunk_type]">{{ chunk.chunk_type }}</span>
                  <span class="chunk-time">{{ fmtTimeShort(chunk.chunk_timestamp) }}</span>
                  <pre class="chunk-content">{{ trunc(chunk.chunk_content, 200) }}</pre>
                </div>
                <div v-if="chunks.length === 0" class="empty-state">暂无数据</div>
              </div>
            </div>
          </div>
        </div>
        <div class="config-sidebar" v-if="showSettings">
          <div class="config-title">抓包设置</div>
          <div class="config-field">
            <label>数据保留（天）</label>
            <input type="number" v-model.number="config.retention_days" class="mac-input" min="1" max="365">
          </div>
          <div class="config-field">
            <label>最大记录数</label>
            <input type="number" v-model.number="config.max_records" class="mac-input" min="100" max="100000">
          </div>
          <button class="save-config-btn" @click="saveConfig">保存配置</button>
        </div>
      </div>
      <div class="capture-panel detail-panel empty-panel" v-else>
        <div class="panel-header"><span>请求详情</span></div>
        <div class="panel-body">
          <div class="empty-state">选择 Turn 查看详情</div>
        </div>
        <div class="config-sidebar" v-if="showSettings">
          <div class="config-title">抓包设置</div>
          <div class="config-field">
            <label>数据保留（天）</label>
            <input type="number" v-model.number="config.retention_days" class="mac-input" min="1" max="365">
          </div>
          <div class="config-field">
            <label>最大记录数</label>
            <input type="number" v-model.number="config.max_records" class="mac-input" min="100" max="100000">
          </div>
          <button class="save-config-btn" @click="saveConfig">保存配置</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import {
  getCaptureEnabled, setCaptureEnabled,
  getCaptureConfig, setCaptureConfig,
  queryActivities, queryTurns, queryRequests, queryChunks,
  exportCaptureJSON, deleteActivity as deleteActivityAPI,
  type CaptureActivity, type CaptureTurn, type CaptureRequest,
  type CaptureChunk, type CaptureConfig as CaptureConfigType,
} from '../../services/capture'

const enabled = ref(false)
const showSettings = ref(false)
const config = ref<CaptureConfigType>({ enabled: false, retention_days: 3, max_records: 1000 })
const activities = ref<CaptureActivity[]>([])
const selectedActivity = ref<CaptureActivity | null>(null)
const turns = ref<CaptureTurn[]>([])
const selectedTurn = ref<CaptureTurn | null>(null)
const requests = ref<CaptureRequest[]>([])
const chunks = ref<CaptureChunk[]>([])

async function loadEnabled() { try { enabled.value = await getCaptureEnabled() } catch(e) { console.error(e) } }
async function loadConfig() { try { config.value = await getCaptureConfig() } catch(e) { console.error(e) } }
async function loadActivites() { try { activities.value = await queryActivities(100, 0) } catch(e) { console.error(e) } }

async function toggleEnabled() { try { await setCaptureEnabled(enabled.value) } catch(e) { console.error(e) } }
async function saveConfig() { try { await setCaptureConfig(config.value); } catch(e) { console.error(e) } }

async function selectActivity(act: CaptureActivity) {
  selectedActivity.value = act; selectedTurn.value = null; turns.value = []; requests.value = []; chunks.value = []
  try { turns.value = await queryTurns(act.activity_id, 100, 0) } catch(e) { console.error(e) }
}

async function selectTurn(turn: CaptureTurn) {
  selectedTurn.value = turn; requests.value = []; chunks.value = []
  try {
    requests.value = await queryRequests(turn.turn_id, 10, 0)
    if (requests.value.length > 0) chunks.value = await queryChunks(requests.value[0].request_id, 1000, 0)
  } catch(e) { console.error(e) }
}

async function deleteActivity(act: CaptureActivity) {
  if (!confirm(`确认删除 Activity ${act.activity_id}?`)) return
  try {
    await deleteActivityAPI(act.activity_id)
    activities.value = activities.value.filter(a => a.activity_id !== act.activity_id)
    if (selectedActivity.value?.activity_id === act.activity_id) {
      selectedActivity.value = null
      turns.value = []
      requests.value = []
      chunks.value = []
    }
  } catch(e) { console.error(e) }
}

async function doExport() {
  if (!selectedTurn.value) return
  try {
    const json = await exportCaptureJSON(selectedTurn.value.turn_id)
    const blob = new Blob([json], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a'); a.href = url; a.download = `capture-${selectedTurn.value.turn_id}.json`
    a.click(); URL.revokeObjectURL(url)
  } catch(e) { console.error(e) }
}

function fmtTime(ts: string): string {
  if (!ts) return '—'
  try { return new Date(ts.includes('T') ? ts : ts + 'Z').toLocaleString('zh-CN', { month:'2-digit', day:'2-digit', hour:'2-digit', minute:'2-digit' }) }
  catch { return ts }
}
function fmtTimeShort(ts: string): string {
  if (!ts) return '—'
  try { return new Date(ts.includes('T') ? ts : ts + 'Z').toLocaleTimeString('zh-CN') }
  catch { return ts }
}
function fmtBytes(b: number): string {
  if (!b) return '0 B'
  const sizes = ['B','KB','MB','GB']; const i = Math.floor(Math.log(b) / Math.log(1024))
  return (b / Math.pow(1024, i)).toFixed(2) + ' ' + sizes[i]
}
function fmtJSON(s: string): string {
  if (!s || s === '{}') return ''
  try { return JSON.stringify(JSON.parse(s), null, 2) } catch { return s }
}
function trunc(s: string, m: number): string {
  return s?.length > m ? s.slice(0, m) + '...' : s || ''
}

onMounted(() => { loadEnabled(); loadConfig(); loadActivites() })
</script>

<style scoped>
.delete-btn {
  position: absolute;
  right: 6px;
  top: 6px;
  background: none;
  border: none;
  color: #999;
  cursor: pointer;
  font-size: 12px;
  padding: 2px 6px;
  border-radius: 4px;
  opacity: 0;
  transition: opacity 0.2s, background 0.2s;
}
.activity-item:hover .delete-btn {
  opacity: 1;
}
.delete-btn:hover {
  background: rgba(255, 60, 60, 0.2);
  color: #ff4444;
}
.activity-item {
  position: relative;
}
.capture-page { display:flex; flex-direction:column; height:100%; background:var(--mac-surface); color:var(--mac-text); }
.capture-header { display:flex; justify-content:space-between; align-items:center; padding:16px 20px; border-bottom:1px solid var(--mac-border); }
.capture-header-left { display:flex; align-items:center; gap:16px; }
.capture-title { font-size:1.25rem; font-weight:700; margin:0; }
.toggle-wrapper { display:flex; align-items:center; gap:8px; cursor:pointer; }
.toggle-wrapper input { display:none; }
.toggle-slider { width:40px; height:22px; background:var(--mac-border); border-radius:11px; position:relative; transition:background .2s; }
.toggle-slider::after { content:''; width:18px; height:18px; background:white; border-radius:50%; position:absolute; top:2px; left:2px; transition:transform .2s; }
.toggle-wrapper input:checked + .toggle-slider { background:var(--mac-accent); }
.toggle-wrapper input:checked + .toggle-slider::after { transform:translateX(18px); }
.toggle-label { font-size:.85rem; color:var(--mac-text-secondary); }
.config-field { display:flex; flex-direction:column; gap:4px; }
.config-field label { font-size:.8rem; color:var(--mac-text-secondary); }
.mac-input { padding:6px 10px; border:1px solid var(--mac-border); border-radius:6px; background:var(--mac-surface); color:var(--mac-text); width:120px; }
.save-config-btn { padding:6px 14px; background:var(--mac-accent); color:white; border:none; border-radius:6px; cursor:pointer; }
.capture-main { display:flex; flex:1; overflow:hidden; }
.capture-panel { display:flex; flex-direction:column; border-right:1px solid var(--mac-border); }
.activity-panel { width:260px; min-width:260px; }
.turn-panel { width:300px; min-width:300px; }
.detail-panel { flex:1; border-right:none; }
.panel-header { display:flex; justify-content:space-between; align-items:center; padding:10px 14px; border-bottom:1px solid var(--mac-border); font-size:.85rem; font-weight:600; }
.panel-count { color:var(--mac-text-secondary); font-weight:400; }
.panel-body { flex:1; overflow-y:auto; padding:4px; }
.activity-item, .turn-item { padding:10px 12px; border-radius:6px; cursor:pointer; transition:background .15s; margin-bottom:2px; }
.activity-item:hover, .turn-item:hover { background:rgba(15,23,42,.06); }
html.dark .activity-item:hover, html.dark .turn-item:hover { background:rgba(255,255,255,.06); }
.activity-item.selected, .turn-item.selected { background:var(--mac-accent); color:white; }
.activity-source { font-weight:600; font-size:.9rem; margin-bottom:4px; }
.activity-meta, .turn-meta { display:flex; gap:8px; font-size:.75rem; color:var(--mac-text-secondary); }
.turn-model { font-weight:500; font-size:.85rem; margin-bottom:4px; }
.config-sidebar { border-top:1px solid var(--mac-border); padding:12px; background:rgba(15,23,42,.03); }
html.dark .config-sidebar { background:rgba(255,255,255,.03); }
.config-sidebar .config-title { font-size:.85rem; font-weight:600; margin-bottom:10px; }
.config-sidebar .config-field { display:flex; flex-direction:column; gap:4px; margin-bottom:10px; }
.config-sidebar .config-field label { font-size:.8rem; color:var(--mac-text-secondary); }
.config-sidebar .mac-input { padding:6px 10px; border:1px solid var(--mac-border); border-radius:6px; background:var(--mac-surface); color:var(--mac-text); width:100%; box-sizing:border-box; }
.config-sidebar .save-config-btn { width:100%; padding:6px 14px; background:var(--mac-accent); color:white; border:none; border-radius:6px; cursor:pointer; font-size:.85rem; }

.config-sidebar { border-top:1px solid var(--mac-border); padding:12px; background:rgba(15,23,42,.03); }
html.dark .config-sidebar { background:rgba(255,255,255,.03); }
.config-sidebar .config-title { font-size:.85rem; font-weight:600; margin-bottom:10px; }
.config-sidebar .config-field { display:flex; flex-direction:column; gap:4px; margin-bottom:10px; }
.config-sidebar .config-field label { font-size:.8rem; color:var(--mac-text-secondary); }
.config-sidebar .mac-input { padding:6px 10px; border:1px solid var(--mac-border); border-radius:6px; background:var(--mac-surface); color:var(--mac-text); width:100%; box-sizing:border-box; }
.config-sidebar .save-config-btn { width:100%; padding:6px 14px; background:var(--mac-accent); color:white; border:none; border-radius:6px; cursor:pointer; font-size:.85rem; }

.capture-settings-btn { padding:4px 10px; border:1px solid var(--mac-border); border-radius:6px; background:transparent; color:var(--mac-text); cursor:pointer; font-size:1rem; line-height:1; }
.capture-settings-btn:hover { background:var(--mac-accent); color:white; border-color:var(--mac-accent); }

.capture-settings-btn { padding:4px 10px; border:1px solid var(--mac-border); border-radius:6px; background:transparent; color:var(--mac-text); cursor:pointer; font-size:1rem; line-height:1; }
.capture-settings-btn:hover { background:var(--mac-accent); color:white; border-color:var(--mac-accent); }

.status-tag { padding:1px 6px; border-radius:4px; font-size:.7rem; font-weight:600; }
.status-tag.success { background:rgba(16,185,129,.15); color:#10b981; }
.status-tag.error { background:rgba(239,68,68,.15); color:#ef4444; }
.status-tag.streaming { background:rgba(59,130,246,.15); color:#3b82f6; }
.status-tag.pending { background:rgba(234,179,8,.15); color:#eab308; }
.empty-state { text-align:center; padding:40px 16px; color:var(--mac-text-secondary); font-size:.85rem; }
.detail-body { padding:12px; }
.request-card { border:1px solid var(--mac-border); border-radius:8px; padding:12px; margin-bottom:12px; }
.request-summary { display:flex; gap:8px; align-items:center; margin-bottom:8px; }
.method-tag { padding:2px 6px; border-radius:4px; background:rgba(59,130,246,.15); color:#3b82f6; font-size:.75rem; font-weight:700; }
.url-text { flex:1; font-size:.8rem; color:var(--mac-text-secondary); overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.status-code { font-size:.8rem; font-weight:600; }
.detail-section { margin:4px 0; font-size:.8rem; }
.detail-section summary { cursor:pointer; color:var(--mac-text-secondary); padding:4px 0; }
.code-block { background:rgba(15,23,42,.05); border-radius:4px; padding:8px; font-size:.75rem; overflow-x:auto; white-space:pre-wrap; word-break:break-all; }
html.dark .code-block { background:rgba(255,255,255,.05); }
.response-summary { display:flex; gap:12px; padding:8px 0; font-size:.8rem; color:var(--mac-text-secondary); }
.chunks-section { margin-top:8px; }
.chunks-header { font-size:.8rem; font-weight:600; margin-bottom:4px; }
.chunks-list { max-height:300px; overflow-y:auto; }
.chunk-item { display:flex; gap:8px; padding:6px 8px; border-bottom:1px solid var(--mac-border); font-size:.75rem; align-items:flex-start; }
.chunk-index { font-weight:600; min-width:30px; color:var(--mac-text-secondary); }
.chunk-type { padding:1px 5px; border-radius:3px; font-weight:600; min-width:40px; text-align:center; }
.chunk-type.text { background:rgba(16,185,129,.15); color:#10b981; }
.chunk-type.tool_use { background:rgba(139,92,246,.15); color:#8b5cf6; }
.chunk-type.error { background:rgba(239,68,68,.15); color:#ef4444; }
.chunk-type.done { background:rgba(59,130,246,.15); color:#3b82f6; }
.chunk-time { min-width:70px; color:var(--mac-text-secondary); }
.chunk-content { flex:1; white-space:pre-wrap; word-break:break-all; background:none; padding:0; margin:0; max-height:60px; overflow-y:auto; }
.export-btn { padding:4px 10px; border:1px solid var(--mac-border); border-radius:4px; background:transparent; color:var(--mac-text); cursor:pointer; font-size:.75rem; }
.export-btn:hover { background:var(--mac-accent); color:white; border-color:var(--mac-accent); }
.empty-panel .panel-body { display:flex; align-items:center; justify-content:center; }
</style>
