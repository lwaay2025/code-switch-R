<template>
  <div class="pricing-editor">
    <div class="editor-header">
      <label class="editor-label">
        <span>{{ $t('components.provider.pricingOverrides.label') }}</span>
        <button
          type="button"
          class="help-icon"
          :data-tooltip="$t('components.provider.pricingOverrides.tooltip')"
        >
          <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
            <path
              d="M8 1a7 7 0 100 14A7 7 0 008 1zm0 13A6 6 0 118 2a6 6 0 010 12zm0-9.5a.75.75 0 01.75.75v4a.75.75 0 01-1.5 0v-4A.75.75 0 018 4.5zm0 7.5a1 1 0 100-2 1 1 0 000 2z"
              fill="currentColor"
            />
          </svg>
        </button>
      </label>
    </div>

    <div v-if="overrideList.length > 0" class="override-list">
      <details v-for="item in overrideList" :key="item.model" class="override-card">
        <summary class="override-summary">
          <code>{{ item.model }}</code>
          <button
            type="button"
            class="mapping-remove"
            :aria-label="$t('components.provider.pricingOverrides.removeModel')"
            @click.prevent="removeOverride(item.model)"
          >
            <svg viewBox="0 0 12 12" width="10" height="10" aria-hidden="true">
              <path d="M3 3l6 6M9 3l-6 6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
            </svg>
          </button>
        </summary>

        <div class="override-grid">
          <label v-for="field in fields" :key="field.key" class="field-item">
            <span>{{ field.label }}</span>
            <BaseInput
              :model-value="readField(item.model, field.key)"
              type="number"
              step="any"
              :placeholder="field.placeholder"
              @update:model-value="updateField(item.model, field.key, $event)"
            />
          </label>
        </div>
      </details>
    </div>

    <div class="add-model-row">
      <BaseInput
        v-model="newModel"
        type="text"
        :placeholder="$t('components.provider.pricingOverrides.modelPlaceholder')"
        @keydown.enter.prevent="addOverride"
      />
      <BaseButton type="button" variant="outline" @click="addOverride">
        {{ $t('components.provider.pricingOverrides.addModel') }}
      </BaseButton>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import BaseInput from './BaseInput.vue'
import BaseButton from './BaseButton.vue'
import { useI18n } from 'vue-i18n'

type PricingOverride = {
  input_cost_per_token?: number
  output_cost_per_token?: number
  output_cost_per_reasoning_token?: number
  cache_creation_input_token_cost?: number
  cache_creation_input_token_cost_above_1hr?: number
  cache_read_input_token_cost?: number
  input_cost_per_token_above_200k_tokens?: number
  input_cost_per_token_above_128k_tokens?: number
  output_cost_per_token_above_200k_tokens?: number
}

interface Props {
  modelValue?: Record<string, PricingOverride>
}

interface Emits {
  (e: 'update:modelValue', value: Record<string, PricingOverride>): void
}

const props = defineProps<Props>()
const emit = defineEmits<Emits>()
const { t } = useI18n()
const newModel = ref('')

const fields = computed(() => [
  { key: 'input_cost_per_token', label: t('components.provider.pricingOverrides.fields.input'), placeholder: '0.00000125' },
  { key: 'output_cost_per_token', label: t('components.provider.pricingOverrides.fields.output'), placeholder: '0.00001' },
  { key: 'output_cost_per_reasoning_token', label: t('components.provider.pricingOverrides.fields.reasoning'), placeholder: '0.00001' },
  { key: 'cache_creation_input_token_cost', label: t('components.provider.pricingOverrides.fields.cacheWrite5m'), placeholder: '0.0000015' },
  { key: 'cache_creation_input_token_cost_above_1hr', label: t('components.provider.pricingOverrides.fields.cacheWrite1h'), placeholder: '0.000006' },
  { key: 'cache_read_input_token_cost', label: t('components.provider.pricingOverrides.fields.cacheRead'), placeholder: '0.000000125' },
  { key: 'input_cost_per_token_above_128k_tokens', label: t('components.provider.pricingOverrides.fields.input128k'), placeholder: '0.000002' },
  { key: 'input_cost_per_token_above_200k_tokens', label: t('components.provider.pricingOverrides.fields.input200k'), placeholder: '0.000003' },
  { key: 'output_cost_per_token_above_200k_tokens', label: t('components.provider.pricingOverrides.fields.output200k'), placeholder: '0.00002' },
])

const overrideList = computed(() => Object.entries(props.modelValue ?? {}).map(([model, values]) => ({ model, values })))

const emitValue = (value: Record<string, PricingOverride>) => emit('update:modelValue', value)

const addOverride = () => {
  const model = newModel.value.trim()
  if (!model) return
  emitValue({ ...(props.modelValue ?? {}), [model]: { ...(props.modelValue?.[model] ?? {}) } })
  newModel.value = ''
}

const removeOverride = (model: string) => {
  const updated = { ...(props.modelValue ?? {}) }
  delete updated[model]
  emitValue(updated)
}

const readField = (model: string, key: string) => {
  const value = (props.modelValue?.[model] as Record<string, number | undefined> | undefined)?.[key]
  return value == null ? '' : String(value)
}

const updateField = (model: string, key: string, rawValue: string | number) => {
  const next = { ...(props.modelValue ?? {}) }
  const current = { ...(next[model] ?? {}) } as Record<string, number | undefined>
  const text = String(rawValue).trim()
  if (!text) {
    delete current[key]
  } else {
    const parsed = Number(text)
    if (!Number.isFinite(parsed) || parsed < 0) return
    current[key] = parsed
  }
  if (Object.keys(current).length === 0) {
    next[model] = {}
  } else {
    next[model] = current
  }
  emitValue(next)
}
</script>

<style scoped>
.pricing-editor { display: flex; flex-direction: column; gap: 12px; }
.editor-header { display: flex; align-items: center; justify-content: space-between; }
.editor-label { display: flex; align-items: center; gap: 6px; font-weight: 500; font-size: 0.875rem; color: var(--foreground); }
.help-icon { display: inline-flex; align-items: center; justify-content: center; padding: 2px; border: none; background: none; color: var(--foreground-muted); cursor: help; border-radius: 4px; }
.override-list { display: flex; flex-direction: column; gap: 8px; }
.override-card { border: 1px solid var(--border); border-radius: 8px; background: var(--background-secondary); padding: 10px; }
.override-summary { display: flex; align-items: center; justify-content: space-between; cursor: pointer; }
.override-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; margin-top: 12px; }
.field-item { display: flex; flex-direction: column; gap: 6px; font-size: 0.8125rem; }
.add-model-row { display: flex; gap: 8px; align-items: center; }
.mapping-remove { display: inline-flex; align-items: center; justify-content: center; padding: 4px; border: none; background: none; color: var(--foreground-muted); cursor: pointer; border-radius: 3px; }
</style>
