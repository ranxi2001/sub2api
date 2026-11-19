import type { LocationQuery } from 'vue-router'
import type { OperationsQuery } from '@/api/admin/profitOperations'
// Preserve supported legacy filters without interpreting arbitrary query fields.
export function profitScopeFromQuery(query: LocationQuery): Partial<OperationsQuery> {
 const out: Record<string,string|number>={}
 const choices:Record<string,string[]>={model_scope:['public','recorded','upstream'],dimension:['upstream','account','model'],sort:['requests','standard_charge','request_income','upstream_cost','gross_profit','gross_margin','estimated_cost','estimated_profit','margin','name'],direction:['asc','desc']}
 for(const key of ['from','until','upstream','model','model_scope','dimension','sort','direction'] as const){
  const value=query[key]
  if(typeof value!=='string')continue
  if((key==='from'||key==='until')&&!Number.isFinite(new Date(value).getTime()))continue
  if(choices[key]&&!choices[key]!.includes(value))continue
  out[key]=value
 }
 for(const key of ['channel_id','account_id','group_id','page','page_size'] as const){
  const n=Number(query[key]);if(query[key]==null||!Number.isSafeInteger(n)||n<0)continue
  if((key==='page'||key==='page_size')&&n<1)continue
  if(key==='page_size'&&n>100)continue
  out[key]=n
 }
 return out as Partial<OperationsQuery>
}
