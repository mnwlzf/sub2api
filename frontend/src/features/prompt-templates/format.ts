/**
 * 文本提示词管理各页面共用的展示格式化函数。
 *
 * 只做展示，不参与任何业务判断。
 */

/** 按当前语言格式化时间戳；无法解析时原样返回，避免显示 Invalid Date。 */
export function formatDateTime(value: string | null | undefined, locale: string): string {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'medium' }).format(date)
}

/** 截断长 SHA-256，保留可辨识前缀。 */
export function shortenSha(value: string | null | undefined, length = 12): string {
  if (!value) return ''
  return value.length > length ? `${value.slice(0, length)}…` : value
}
