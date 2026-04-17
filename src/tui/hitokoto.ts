/**
 * Hitokoto 一言服务 — 每次启动获取新的一言。
 *
 * @author 真心
 */

export interface Hitokoto {
  readonly text: string;
  readonly from: string;
  readonly author: string | null;
}

const API_URL = "https://v1.hitokoto.cn/?c=a&c=b&c=d&encode=json";

/** 异步获取一言 */
export async function fetchHitokoto(): Promise<Hitokoto | null> {
  try {
    const resp = await fetch(API_URL, { signal: AbortSignal.timeout(3000) });
    if (!resp.ok) return null;
    const data = (await resp.json()) as {
      readonly hitokoto: string;
      readonly from: string;
      readonly from_who: string | null;
    };
    return {
      text: data.hitokoto,
      from: data.from,
      author: data.from_who,
    };
  } catch {
    return null;
  }
}
