/**
 * 通用对象类型守卫。
 *
 * @author 真心
 */

/**
 * 判断一个值是否为普通对象记录。
 */
export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
