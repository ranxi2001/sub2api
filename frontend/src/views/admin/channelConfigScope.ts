import type { LocationQuery } from 'vue-router'
import type { OperationsQuery } from '@/api/admin/profitOperations'
// Preserve supported legacy filters without interpreting arbitrary query fields.
export function profitScopeFromQuery(query: LocationQuery): Partial<OperationsQuery> {
 const out: Record<string,string|number>={}
 for(const key of ['from','until','upstream','model','model_scope','dimension','sort','direction'] as const){if(typeof query[key]==='string')out[key]=query[key] as string}
 for(const key of ['channel_id','account_id','group_id','page','page_size'] as const){const n=Number(query[key]);if(query[key]!=null&&Number.isSafeInteger(n)&&n>=0)out[key]=n}
 return out as Partial<OperationsQuery>
}
