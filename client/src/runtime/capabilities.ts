export interface RuntimeCapabilities {upload:boolean;nativeTransfers:boolean;profileEdit:boolean;adminWrite:boolean;updates:boolean;maxDownloadBytes:number}
export const browserCapabilities:RuntimeCapabilities={upload:false,nativeTransfers:false,profileEdit:false,adminWrite:false,updates:false,maxDownloadBytes:1_000_000_000};
export const nativeCapabilities:RuntimeCapabilities={upload:true,nativeTransfers:true,profileEdit:true,adminWrite:true,updates:true,maxDownloadBytes:Infinity};
