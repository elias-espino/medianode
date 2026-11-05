package dlna

import (
	"encoding/xml"
	"strings"
)

const xmlHeader = `<?xml version="1.0" encoding="utf-8"?>` + "\n"

func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// deviceDescription es la descripción UPnP del dispositivo (UDA 1.0 §2.3):
// un MediaServer:1 con ContentDirectory y ConnectionManager, anunciado como
// servidor DLNA (DMS-1.50).
func deviceDescription(name, version, uuid string) []byte {
	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<root xmlns="urn:schemas-upnp-org:device-1-0" xmlns:dlna="urn:schemas-dlna-org:device-1-0">`)
	b.WriteString(`<specVersion><major>1</major><minor>0</minor></specVersion><device>`)
	b.WriteString(`<dlna:X_DLNADOC>DMS-1.50</dlna:X_DLNADOC>`)
	b.WriteString(`<deviceType>` + deviceTypeURN + `</deviceType>`)
	b.WriteString(`<friendlyName>` + esc(name) + `</friendlyName>`)
	b.WriteString(`<manufacturer>MediaNode</manufacturer>`)
	b.WriteString(`<manufacturerURL>https://github.com/elias-espino/medianode</manufacturerURL>`)
	b.WriteString(`<modelDescription>MediaNode media server</modelDescription>`)
	b.WriteString(`<modelName>MediaNode</modelName>`)
	b.WriteString(`<modelNumber>` + esc(version) + `</modelNumber>`)
	b.WriteString(`<UDN>uuid:` + esc(uuid) + `</UDN>`)
	b.WriteString(`<serviceList>`)
	b.WriteString(service(cdsTypeURN, "urn:upnp-org:serviceId:ContentDirectory", "cds"))
	b.WriteString(service(cmsTypeURN, "urn:upnp-org:serviceId:ConnectionManager", "cms"))
	b.WriteString(`</serviceList></device></root>`)
	return []byte(b.String())
}

func service(typ, id, short string) string {
	return `<service><serviceType>` + typ + `</serviceType><serviceId>` + id + `</serviceId>` +
		`<SCPDURL>/dlna/` + short + `.xml</SCPDURL>` +
		`<controlURL>/dlna/` + short + `/control</controlURL>` +
		`<eventSubURL>/dlna/` + short + `/event</eventSubURL></service>`
}

// scpd construye la descripción de un servicio (UDA 1.0 §2.5).
type action struct {
	name string
	args []arg
}

type arg struct {
	name, dir, related string
}

type stateVar struct {
	name, typ string
	events    bool
	values    []string
}

func scpd(actions []action, vars []stateVar) []byte {
	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<scpd xmlns="urn:schemas-upnp-org:service-1-0"><specVersion><major>1</major><minor>0</minor></specVersion><actionList>`)
	for _, a := range actions {
		b.WriteString(`<action><name>` + a.name + `</name><argumentList>`)
		for _, x := range a.args {
			b.WriteString(`<argument><name>` + x.name + `</name><direction>` + x.dir + `</direction><relatedStateVariable>` + x.related + `</relatedStateVariable></argument>`)
		}
		b.WriteString(`</argumentList></action>`)
	}
	b.WriteString(`</actionList><serviceStateTable>`)
	for _, v := range vars {
		ev := "no"
		if v.events {
			ev = "yes"
		}
		b.WriteString(`<stateVariable sendEvents="` + ev + `"><name>` + v.name + `</name><dataType>` + v.typ + `</dataType>`)
		if len(v.values) > 0 {
			b.WriteString(`<allowedValueList>`)
			for _, val := range v.values {
				b.WriteString(`<allowedValue>` + val + `</allowedValue>`)
			}
			b.WriteString(`</allowedValueList>`)
		}
		b.WriteString(`</stateVariable>`)
	}
	b.WriteString(`</serviceStateTable></scpd>`)
	return []byte(b.String())
}

var cdsSCPD = scpd(
	[]action{
		{"GetSearchCapabilities", []arg{{"SearchCaps", "out", "SearchCapabilities"}}},
		{"GetSortCapabilities", []arg{{"SortCaps", "out", "SortCapabilities"}}},
		{"GetSystemUpdateID", []arg{{"Id", "out", "SystemUpdateID"}}},
		{"Browse", []arg{
			{"ObjectID", "in", "A_ARG_TYPE_ObjectID"}, {"BrowseFlag", "in", "A_ARG_TYPE_BrowseFlag"},
			{"Filter", "in", "A_ARG_TYPE_Filter"}, {"StartingIndex", "in", "A_ARG_TYPE_Index"},
			{"RequestedCount", "in", "A_ARG_TYPE_Count"}, {"SortCriteria", "in", "A_ARG_TYPE_SortCriteria"},
			{"Result", "out", "A_ARG_TYPE_Result"}, {"NumberReturned", "out", "A_ARG_TYPE_Count"},
			{"TotalMatches", "out", "A_ARG_TYPE_Count"}, {"UpdateID", "out", "A_ARG_TYPE_UpdateID"},
		}},
		{"Search", []arg{
			{"ContainerID", "in", "A_ARG_TYPE_ObjectID"}, {"SearchCriteria", "in", "A_ARG_TYPE_SearchCriteria"},
			{"Filter", "in", "A_ARG_TYPE_Filter"}, {"StartingIndex", "in", "A_ARG_TYPE_Index"},
			{"RequestedCount", "in", "A_ARG_TYPE_Count"}, {"SortCriteria", "in", "A_ARG_TYPE_SortCriteria"},
			{"Result", "out", "A_ARG_TYPE_Result"}, {"NumberReturned", "out", "A_ARG_TYPE_Count"},
			{"TotalMatches", "out", "A_ARG_TYPE_Count"}, {"UpdateID", "out", "A_ARG_TYPE_UpdateID"},
		}},
	},
	[]stateVar{
		{"A_ARG_TYPE_ObjectID", "string", false, nil},
		{"A_ARG_TYPE_Result", "string", false, nil},
		{"A_ARG_TYPE_BrowseFlag", "string", false, []string{"BrowseMetadata", "BrowseDirectChildren"}},
		{"A_ARG_TYPE_Filter", "string", false, nil},
		{"A_ARG_TYPE_SortCriteria", "string", false, nil},
		{"A_ARG_TYPE_SearchCriteria", "string", false, nil},
		{"A_ARG_TYPE_Index", "ui4", false, nil},
		{"A_ARG_TYPE_Count", "ui4", false, nil},
		{"A_ARG_TYPE_UpdateID", "ui4", false, nil},
		{"SearchCapabilities", "string", false, nil},
		{"SortCapabilities", "string", false, nil},
		{"SystemUpdateID", "ui4", true, nil},
	},
)

var cmsSCPD = scpd(
	[]action{
		{"GetProtocolInfo", []arg{{"Source", "out", "SourceProtocolInfo"}, {"Sink", "out", "SinkProtocolInfo"}}},
		{"GetCurrentConnectionIDs", []arg{{"ConnectionIDs", "out", "CurrentConnectionIDs"}}},
		{"GetCurrentConnectionInfo", []arg{
			{"ConnectionID", "in", "A_ARG_TYPE_ConnectionID"}, {"RcsID", "out", "A_ARG_TYPE_RcsID"},
			{"AVTransportID", "out", "A_ARG_TYPE_AVTransportID"}, {"ProtocolInfo", "out", "A_ARG_TYPE_ProtocolInfo"},
			{"PeerConnectionManager", "out", "A_ARG_TYPE_ConnectionManager"}, {"PeerConnectionID", "out", "A_ARG_TYPE_ConnectionID"},
			{"Direction", "out", "A_ARG_TYPE_Direction"}, {"Status", "out", "A_ARG_TYPE_ConnectionStatus"},
		}},
	},
	[]stateVar{
		{"SourceProtocolInfo", "string", true, nil},
		{"SinkProtocolInfo", "string", true, nil},
		{"CurrentConnectionIDs", "string", true, nil},
		{"A_ARG_TYPE_ConnectionStatus", "string", false, []string{"OK", "ContentFormatMismatch", "InsufficientBandwidth", "UnreliableChannel", "Unknown"}},
		{"A_ARG_TYPE_ConnectionManager", "string", false, nil},
		{"A_ARG_TYPE_Direction", "string", false, []string{"Input", "Output"}},
		{"A_ARG_TYPE_ProtocolInfo", "string", false, nil},
		{"A_ARG_TYPE_ConnectionID", "i4", false, nil},
		{"A_ARG_TYPE_AVTransportID", "i4", false, nil},
		{"A_ARG_TYPE_RcsID", "i4", false, nil},
	},
)
