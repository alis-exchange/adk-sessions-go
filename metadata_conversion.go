package sessions

import (
	"time"

	"cloud.google.com/go/civil"
	"google.golang.org/genai"
	"google.golang.org/genproto/googleapis/type/date"

	pb "go.alis.build/common/alis/adk/sessions"
)

// Model response metadata (grounding, usage, citations, logprobs).
//
// Every converter returns nil for nil input and leaves absent repeated
// fields nil rather than empty, see the note at the top of conversion.go.

func groundingMetadataToProto(in *genai.GroundingMetadata) *pb.GroundingMetadata {
	if in == nil {
		return nil
	}
	out := &pb.GroundingMetadata{
		WebSearchQueries:             cloneOrNil(in.WebSearchQueries),
		RetrievalQueries:             cloneOrNil(in.RetrievalQueries),
		SearchEntryPoint:             searchEntryPointToProto(in.SearchEntryPoint),
		RetrievalMetadata:            retrievalMetadataToProto(in.RetrievalMetadata),
		GoogleMapsWidgetContextToken: in.GoogleMapsWidgetContextToken,
	}
	for _, chunk := range in.GroundingChunks {
		out.GroundingChunks = append(out.GroundingChunks, groundingChunkToProto(chunk))
	}
	for _, support := range in.GroundingSupports {
		out.GroundingSupports = append(out.GroundingSupports, groundingSupportToProto(support))
	}
	for _, uri := range in.SourceFlaggingUris {
		out.SourceFlaggingUris = append(out.SourceFlaggingUris, &pb.GroundingMetadata_SourceFlaggingUri{
			SourceId:       uri.SourceID,
			FlagContentUri: uri.FlagContentURI,
		})
	}
	return out
}

func groundingMetadataFromProto(in *pb.GroundingMetadata) *genai.GroundingMetadata {
	if in == nil {
		return nil
	}
	out := &genai.GroundingMetadata{
		WebSearchQueries:             cloneOrNil(in.GetWebSearchQueries()),
		RetrievalQueries:             cloneOrNil(in.GetRetrievalQueries()),
		SearchEntryPoint:             searchEntryPointFromProto(in.GetSearchEntryPoint()),
		RetrievalMetadata:            retrievalMetadataFromProto(in.GetRetrievalMetadata()),
		GoogleMapsWidgetContextToken: in.GetGoogleMapsWidgetContextToken(),
	}
	for _, chunk := range in.GetGroundingChunks() {
		out.GroundingChunks = append(out.GroundingChunks, groundingChunkFromProto(chunk))
	}
	for _, support := range in.GetGroundingSupports() {
		out.GroundingSupports = append(out.GroundingSupports, groundingSupportFromProto(support))
	}
	for _, uri := range in.GetSourceFlaggingUris() {
		out.SourceFlaggingUris = append(out.SourceFlaggingUris, &genai.GroundingMetadataSourceFlaggingURI{
			SourceID:       uri.GetSourceId(),
			FlagContentURI: uri.GetFlagContentUri(),
		})
	}
	return out
}

func groundingChunkToProto(in *genai.GroundingChunk) *pb.GroundingChunk {
	out := &pb.GroundingChunk{}
	if in == nil {
		return out
	}
	switch {
	case in.Web != nil:
		out.ChunkType = &pb.GroundingChunk_Web_{Web: &pb.GroundingChunk_Web{
			Domain: in.Web.Domain,
			Uri:    in.Web.URI,
			Title:  in.Web.Title,
		}}
	case in.RetrievedContext != nil:
		rc := &pb.GroundingChunk_RetrievedContext{
			Uri:          in.RetrievedContext.URI,
			Title:        in.RetrievedContext.Title,
			Text:         in.RetrievedContext.Text,
			DocumentName: in.RetrievedContext.DocumentName,
		}
		if in.RetrievedContext.RAGChunk != nil {
			rc.ContextDetails = &pb.GroundingChunk_RetrievedContext_RagChunk{RagChunk: ragChunkToProto(in.RetrievedContext.RAGChunk)}
		}
		out.ChunkType = &pb.GroundingChunk_RetrievedContext_{RetrievedContext: rc}
	case in.Maps != nil:
		out.ChunkType = &pb.GroundingChunk_Maps_{Maps: &pb.GroundingChunk_Maps{
			Uri:                in.Maps.URI,
			Title:              in.Maps.Title,
			Text:               in.Maps.Text,
			PlaceId:            in.Maps.PlaceID,
			PlaceAnswerSources: placeAnswerSourcesToProto(in.Maps.PlaceAnswerSources),
		}}
	}
	return out
}

func groundingChunkFromProto(in *pb.GroundingChunk) *genai.GroundingChunk {
	out := &genai.GroundingChunk{}
	switch chunk := in.GetChunkType().(type) {
	case *pb.GroundingChunk_Web_:
		out.Web = &genai.GroundingChunkWeb{
			Domain: chunk.Web.GetDomain(),
			URI:    chunk.Web.GetUri(),
			Title:  chunk.Web.GetTitle(),
		}
	case *pb.GroundingChunk_RetrievedContext_:
		out.RetrievedContext = &genai.GroundingChunkRetrievedContext{
			URI:          chunk.RetrievedContext.GetUri(),
			Title:        chunk.RetrievedContext.GetTitle(),
			Text:         chunk.RetrievedContext.GetText(),
			DocumentName: chunk.RetrievedContext.GetDocumentName(),
			RAGChunk:     ragChunkFromProto(chunk.RetrievedContext.GetRagChunk()),
		}
	case *pb.GroundingChunk_Maps_:
		out.Maps = &genai.GroundingChunkMaps{
			URI:                chunk.Maps.GetUri(),
			Title:              chunk.Maps.GetTitle(),
			Text:               chunk.Maps.GetText(),
			PlaceID:            chunk.Maps.GetPlaceId(),
			PlaceAnswerSources: placeAnswerSourcesFromProto(chunk.Maps.GetPlaceAnswerSources()),
		}
	}
	return out
}

func placeAnswerSourcesToProto(in *genai.GroundingChunkMapsPlaceAnswerSources) *pb.GroundingChunk_Maps_PlaceAnswerSources {
	if in == nil {
		return nil
	}
	out := &pb.GroundingChunk_Maps_PlaceAnswerSources{FlagContentUri: in.FlagContentURI}
	for _, snippet := range in.ReviewSnippets {
		out.ReviewSnippets = append(out.ReviewSnippets, &pb.GroundingChunk_Maps_PlaceAnswerSources_ReviewSnippet{
			ReviewId:                       snippet.ReviewID,
			GoogleMapsUri:                  snippet.GoogleMapsURI,
			Title:                          snippet.Title,
			Review:                         snippet.Review,
			FlagContentUri:                 snippet.FlagContentURI,
			RelativePublishTimeDescription: snippet.RelativePublishTimeDescription,
		})
	}
	return out
}

func placeAnswerSourcesFromProto(in *pb.GroundingChunk_Maps_PlaceAnswerSources) *genai.GroundingChunkMapsPlaceAnswerSources {
	if in == nil {
		return nil
	}
	out := &genai.GroundingChunkMapsPlaceAnswerSources{FlagContentURI: in.GetFlagContentUri()}
	for _, snippet := range in.GetReviewSnippets() {
		out.ReviewSnippets = append(out.ReviewSnippets, &genai.GroundingChunkMapsPlaceAnswerSourcesReviewSnippet{
			ReviewID:                       snippet.GetReviewId(),
			GoogleMapsURI:                  snippet.GetGoogleMapsUri(),
			Title:                          snippet.GetTitle(),
			Review:                         snippet.GetReview(),
			FlagContentURI:                 snippet.GetFlagContentUri(),
			RelativePublishTimeDescription: snippet.GetRelativePublishTimeDescription(),
		})
	}
	return out
}

func ragChunkToProto(in *genai.RAGChunk) *pb.RagChunk {
	if in == nil {
		return nil
	}
	out := &pb.RagChunk{Text: in.Text}
	if in.PageSpan != nil {
		out.PageSpan = &pb.RagChunk_PageSpan{FirstPage: in.PageSpan.FirstPage, LastPage: in.PageSpan.LastPage}
	}
	return out
}

func ragChunkFromProto(in *pb.RagChunk) *genai.RAGChunk {
	if in == nil {
		return nil
	}
	out := &genai.RAGChunk{Text: in.GetText()}
	if span := in.GetPageSpan(); span != nil {
		out.PageSpan = &genai.RAGChunkPageSpan{FirstPage: span.GetFirstPage(), LastPage: span.GetLastPage()}
	}
	return out
}

func groundingSupportToProto(in *genai.GroundingSupport) *pb.GroundingSupport {
	out := &pb.GroundingSupport{}
	if in == nil {
		return out
	}
	out.GroundingChunkIndices = cloneOrNil(in.GroundingChunkIndices)
	out.ConfidenceScores = cloneOrNil(in.ConfidenceScores)
	if in.Segment != nil {
		out.Segment = &pb.Segment{
			PartIndex:  in.Segment.PartIndex,
			StartIndex: in.Segment.StartIndex,
			EndIndex:   in.Segment.EndIndex,
			Text:       in.Segment.Text,
		}
	}
	return out
}

func groundingSupportFromProto(in *pb.GroundingSupport) *genai.GroundingSupport {
	out := &genai.GroundingSupport{
		GroundingChunkIndices: cloneOrNil(in.GetGroundingChunkIndices()),
		ConfidenceScores:      cloneOrNil(in.GetConfidenceScores()),
	}
	if segment := in.GetSegment(); segment != nil {
		out.Segment = &genai.Segment{
			PartIndex:  segment.GetPartIndex(),
			StartIndex: segment.GetStartIndex(),
			EndIndex:   segment.GetEndIndex(),
			Text:       segment.GetText(),
		}
	}
	return out
}

func searchEntryPointToProto(in *genai.SearchEntryPoint) *pb.SearchEntryPoint {
	if in == nil {
		return nil
	}
	return &pb.SearchEntryPoint{RenderedContent: in.RenderedContent, SdkBlob: bytesOrNil(in.SDKBlob)}
}

func searchEntryPointFromProto(in *pb.SearchEntryPoint) *genai.SearchEntryPoint {
	if in == nil {
		return nil
	}
	return &genai.SearchEntryPoint{RenderedContent: in.GetRenderedContent(), SDKBlob: bytesOrNil(in.GetSdkBlob())}
}

func retrievalMetadataToProto(in *genai.RetrievalMetadata) *pb.RetrievalMetadata {
	if in == nil {
		return nil
	}
	return &pb.RetrievalMetadata{GoogleSearchDynamicRetrievalScore: in.GoogleSearchDynamicRetrievalScore}
}

func retrievalMetadataFromProto(in *pb.RetrievalMetadata) *genai.RetrievalMetadata {
	if in == nil {
		return nil
	}
	return &genai.RetrievalMetadata{GoogleSearchDynamicRetrievalScore: in.GetGoogleSearchDynamicRetrievalScore()}
}

func usageMetadataToProto(in *genai.GenerateContentResponseUsageMetadata) *pb.UsageMetadata {
	if in == nil {
		return nil
	}
	return &pb.UsageMetadata{
		CacheTokensDetails:         modalityTokenCountsToProto(in.CacheTokensDetails),
		CachedContentTokenCount:    in.CachedContentTokenCount,
		CandidatesTokenCount:       in.CandidatesTokenCount,
		CandidatesTokensDetails:    modalityTokenCountsToProto(in.CandidatesTokensDetails),
		PromptTokenCount:           in.PromptTokenCount,
		PromptTokensDetails:        modalityTokenCountsToProto(in.PromptTokensDetails),
		ThoughtsTokenCount:         in.ThoughtsTokenCount,
		ToolUsePromptTokenCount:    in.ToolUsePromptTokenCount,
		ToolUsePromptTokensDetails: modalityTokenCountsToProto(in.ToolUsePromptTokensDetails),
		TotalTokenCount:            in.TotalTokenCount,
		TrafficType:                trafficTypeToProto(in.TrafficType),
	}
}

func usageMetadataFromProto(in *pb.UsageMetadata) *genai.GenerateContentResponseUsageMetadata {
	if in == nil {
		return nil
	}
	return &genai.GenerateContentResponseUsageMetadata{
		CacheTokensDetails:         modalityTokenCountsFromProto(in.GetCacheTokensDetails()),
		CachedContentTokenCount:    in.GetCachedContentTokenCount(),
		CandidatesTokenCount:       in.GetCandidatesTokenCount(),
		CandidatesTokensDetails:    modalityTokenCountsFromProto(in.GetCandidatesTokensDetails()),
		PromptTokenCount:           in.GetPromptTokenCount(),
		PromptTokensDetails:        modalityTokenCountsFromProto(in.GetPromptTokensDetails()),
		ThoughtsTokenCount:         in.GetThoughtsTokenCount(),
		ToolUsePromptTokenCount:    in.GetToolUsePromptTokenCount(),
		ToolUsePromptTokensDetails: modalityTokenCountsFromProto(in.GetToolUsePromptTokensDetails()),
		TotalTokenCount:            in.GetTotalTokenCount(),
		TrafficType:                trafficTypeFromProto(in.GetTrafficType()),
	}
}

func modalityTokenCountsToProto(in []*genai.ModalityTokenCount) []*pb.ModalityTokenCount {
	var out []*pb.ModalityTokenCount
	for _, count := range in {
		converted := &pb.ModalityTokenCount{}
		if count != nil {
			converted.Modality = mediaModalityToProto(count.Modality)
			converted.TokenCount = count.TokenCount
		}
		out = append(out, converted)
	}
	return out
}

func modalityTokenCountsFromProto(in []*pb.ModalityTokenCount) []*genai.ModalityTokenCount {
	var out []*genai.ModalityTokenCount
	for _, count := range in {
		out = append(out, &genai.ModalityTokenCount{
			Modality:   mediaModalityFromProto(count.GetModality()),
			TokenCount: count.GetTokenCount(),
		})
	}
	return out
}

func citationMetadataToProto(in *genai.CitationMetadata) *pb.CitationMetadata {
	if in == nil {
		return nil
	}
	out := &pb.CitationMetadata{}
	for _, citation := range in.Citations {
		converted := &pb.CitationMetadata_Citation{}
		if citation != nil {
			converted.StartIndex = citation.StartIndex
			converted.EndIndex = citation.EndIndex
			converted.License = citation.License
			converted.PublicationDate = dateToProto(citation.PublicationDate)
			converted.Title = citation.Title
			converted.Uri = citation.URI
		}
		out.Citations = append(out.Citations, converted)
	}
	return out
}

func citationMetadataFromProto(in *pb.CitationMetadata) *genai.CitationMetadata {
	if in == nil {
		return nil
	}
	out := &genai.CitationMetadata{}
	for _, citation := range in.GetCitations() {
		out.Citations = append(out.Citations, &genai.Citation{
			StartIndex:      citation.GetStartIndex(),
			EndIndex:        citation.GetEndIndex(),
			License:         citation.GetLicense(),
			PublicationDate: dateFromProto(citation.GetPublicationDate()),
			Title:           citation.GetTitle(),
			URI:             citation.GetUri(),
		})
	}
	return out
}

// dateToProto keeps the zero civil.Date (genai's "no date") as an absent
// proto field instead of the invalid date 0000-00-00.
func dateToProto(d civil.Date) *date.Date {
	if d == (civil.Date{}) {
		return nil
	}
	return &date.Date{Year: int32(d.Year), Month: int32(d.Month), Day: int32(d.Day)}
}

func dateFromProto(d *date.Date) civil.Date {
	if d == nil {
		return civil.Date{}
	}
	return civil.Date{Year: int(d.GetYear()), Month: time.Month(d.GetMonth()), Day: int(d.GetDay())}
}

func logprobsResultToProto(in *genai.LogprobsResult) *pb.LogprobsResult {
	if in == nil {
		return nil
	}
	out := &pb.LogprobsResult{ChosenCandidates: logprobsCandidatesToProto(in.ChosenCandidates)}
	for _, top := range in.TopCandidates {
		converted := &pb.LogprobsResultTopCandidates{}
		if top != nil {
			converted.Candidates = logprobsCandidatesToProto(top.Candidates)
		}
		out.TopCandidates = append(out.TopCandidates, converted)
	}
	return out
}

func logprobsResultFromProto(in *pb.LogprobsResult) *genai.LogprobsResult {
	if in == nil {
		return nil
	}
	out := &genai.LogprobsResult{ChosenCandidates: logprobsCandidatesFromProto(in.GetChosenCandidates())}
	for _, top := range in.GetTopCandidates() {
		out.TopCandidates = append(out.TopCandidates, &genai.LogprobsResultTopCandidates{
			Candidates: logprobsCandidatesFromProto(top.GetCandidates()),
		})
	}
	return out
}

func logprobsCandidatesToProto(in []*genai.LogprobsResultCandidate) []*pb.LogprobsResultCandidate {
	var out []*pb.LogprobsResultCandidate
	for _, candidate := range in {
		converted := &pb.LogprobsResultCandidate{}
		if candidate != nil {
			converted.LogProbability = candidate.LogProbability
			converted.Token = candidate.Token
			converted.TokenId = candidate.TokenID
		}
		out = append(out, converted)
	}
	return out
}

func logprobsCandidatesFromProto(in []*pb.LogprobsResultCandidate) []*genai.LogprobsResultCandidate {
	var out []*genai.LogprobsResultCandidate
	for _, candidate := range in {
		out = append(out, &genai.LogprobsResultCandidate{
			LogProbability: candidate.GetLogProbability(),
			Token:          candidate.GetToken(),
			TokenID:        candidate.GetTokenId(),
		})
	}
	return out
}
