import { DataSourceJsonData } from '@grafana/data';
import { DataQuery } from '@grafana/schema';
export interface PromhashQuery extends DataQuery { queryType: 'app_path' | 'impact'; app?: string; device?: string; ifName?: string; }
export interface PromhashOptions extends DataSourceJsonData { apiUrl?: string; }
